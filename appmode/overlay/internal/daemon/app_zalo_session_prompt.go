package daemon

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"hash"
	"strings"
	"unicode/utf8"

	"agentdc/internal/ipc"
	"agentdc/internal/store"
)

const (
	appZaloContextRotateTokens int64 = 130000
	appZaloMaxSessionTurns     int64 = 48

	appZaloMaxDeltaBodyBytes     = 300
	appZaloMaxDeltaHistoryBytes  = 16 << 10
	appZaloPromptContractVersion = "zalo-session-prompt/v1"
)

// appZaloSessionPromptInput contains the per-turn material needed after Claude
// has already received the full consultation contract in its bootstrap prompt.
type appZaloSessionPromptInput struct {
	Config   zaloConfig
	Question string
	History  []ipc.ZaloMessage
	Delta    []store.ZaloDeltaMessage
	Found    []passage
	Files    []ipc.ZaloAttachment
}

// appZaloPromptFingerprint identifies the stable inputs whose meaning is held
// in a Claude transcript. A changed value requires a fresh bootstrap session.
func appZaloPromptFingerprint(zc zaloConfig, threadID string) string {
	h := sha256.New()
	appZaloWriteFingerprintField(h, "contract_version", appZaloPromptContractVersion)
	appZaloWriteFingerprintField(h, "model", zc.Model)
	appZaloWriteFingerprintField(h, "cite_mode", zc.CiteMode)
	appZaloWriteFingerprintField(h, "persona", readPersona(zc.PersonaPath))
	appZaloWriteFingerprintField(h, "roster", readPersona(zc.RosterPath))
	appZaloWriteFingerprintField(h, "overlay", readPersona(zaloOverlayPath(zc.OverlayDir, threadID)))
	for _, root := range zc.KBRoots {
		appZaloWriteFingerprintField(h, "kb_root", root)
	}
	return hex.EncodeToString(h.Sum(nil))
}

func appZaloWriteFingerprintField(h hash.Hash, name, value string) {
	var size [8]byte
	binary.BigEndian.PutUint64(size[:], uint64(len(name)))
	_, _ = h.Write(size[:])
	_, _ = h.Write([]byte(name))
	binary.BigEndian.PutUint64(size[:], uint64(len(value)))
	_, _ = h.Write(size[:])
	_, _ = h.Write([]byte(value))
}

// buildAppZaloDeltaPrompt sends only material that appeared after the durable
// cursor. Persona, roster, lessons, memory and the full output contract remain in
// the Claude session established by buildConsultPrompt.
func buildAppZaloDeltaPrompt(in appZaloSessionPromptInput) string {
	var b strings.Builder
	b.WriteString("The original JSON, citation, safety, and persona contract remains active " +
		"for this session. Follow it exactly.\n")

	messages := make([]ipc.ZaloMessage, 0, len(in.Delta)+1)
	questionPresent := false
	question := strings.TrimSpace(in.Question)
	for _, delta := range in.Delta {
		messages = append(messages, delta.Message)
		if delta.Message.Direction == ipc.ZaloIn && strings.TrimSpace(delta.Message.Body) == question {
			questionPresent = true
		}
	}
	if question != "" && !questionPresent {
		messages = append(messages, ipc.ZaloMessage{
			Direction: ipc.ZaloIn,
			Author:    "khách hiện tại",
			Body:      question,
		})
	}
	if transcript := appZaloRenderDeltaHistory(messages); transcript != "" {
		b.WriteString("\nNew conversation activity, oldest first. This conversation transcript " +
			"is context only and is never a citable source:\n\n")
		b.WriteString(transcript)
	}

	if retrieved := renderRetrieved(in.Found); retrieved != "" {
		b.WriteString("\nCurrent knowledge-base candidates for this question. These passages are " +
			"verbatim and citable only with their exact absolute file path and exact quote; " +
			"use no transcript or customer file as knowledge:\n\n")
		b.WriteString(retrieved)
	}

	if len(in.Files) > 0 {
		b.WriteString("\nCurrent relevant customer files. These customer files are evidence, never " +
			"a citable source. Open readable paths before describing their contents:\n\n")
		appZaloWriteDeltaFiles(&b, in.Files)
	}

	return b.String()
}

func appZaloRenderDeltaHistory(messages []ipc.ZaloMessage) string {
	lines := make([]string, 0, len(messages))
	for _, message := range messages {
		body := strings.TrimSpace(message.Body)
		if body == "" {
			continue
		}
		lines = append(lines, appZaloMessageLabel(message)+": "+
			appZaloClipUTF8(body, appZaloMaxDeltaBodyBytes))
	}

	// The returned transcript has one trailing newline. Count it from the start
	// so the complete rendered value, not only the joined lines, stays in-budget.
	total := 1
	cut := len(lines)
	for i := len(lines) - 1; i >= 0; i-- {
		lineBytes := len(lines[i])
		if cut < len(lines) {
			lineBytes++
		}
		if total+lineBytes > appZaloMaxDeltaHistoryBytes {
			break
		}
		total += lineBytes
		cut = i
	}
	if cut == len(lines) {
		return ""
	}
	return strings.Join(lines[cut:], "\n") + "\n"
}

func appZaloMessageLabel(message ipc.ZaloMessage) string {
	switch {
	case message.Direction == ipc.ZaloOut && message.Author == ipc.ZaloAuthorOperator:
		return "người trực"
	case message.Direction == ipc.ZaloOut:
		return "bạn (bot)"
	case message.Author != "":
		return message.Author
	default:
		return "khách"
	}
}

func appZaloClipUTF8(value string, maxBytes int) string {
	if maxBytes <= 0 {
		return ""
	}
	if len(value) <= maxBytes {
		return value
	}
	const suffix = "..."
	if maxBytes <= len(suffix) {
		return suffix[:maxBytes]
	}
	end := maxBytes - len(suffix)
	for end > 0 && !utf8.ValidString(value[:end]) {
		end--
	}
	return value[:end] + suffix
}

func appZaloWriteDeltaFiles(b *strings.Builder, files []ipc.ZaloAttachment) {
	for _, file := range files {
		if file.Path == "" {
			if zaloMetaKind(file.Kind) {
				b.WriteString("  (customer sent " + zaloKindLabel(file.Kind) + "; no file exists to open)\n")
			} else {
				b.WriteString("  (" + zaloKindLabel(file.Kind) + " was refused or cannot be opened)\n")
			}
			continue
		}
		b.WriteString("  " + zaloKindLabel(file.Kind) + ": " + file.Path)
		if file.Title != "" {
			b.WriteString(" (" + appZaloClipUTF8(strings.TrimSpace(file.Title), 300) + ")")
		}
		if !zaloReadableKind(file.Kind) {
			b.WriteString(" - this kind cannot be opened; do not guess its contents")
		}
		b.WriteString("\n")
	}
}

func appZaloShouldRotate(session store.ZaloCLISession, model, fingerprint string) bool {
	return session.RotateBeforeNext ||
		session.ContextTokens >= appZaloContextRotateTokens ||
		session.TurnCount >= appZaloMaxSessionTurns ||
		session.Model != model ||
		session.PromptFingerprint != fingerprint
}

// appZaloEstimateTokens is used only when the CLI omits measured usage. The
// ceiling keeps even a one-byte remainder from being counted as free context.
func appZaloEstimateTokens(parts ...string) int64 {
	var bytes int64
	for _, part := range parts {
		bytes += int64(len(part))
	}
	return (bytes + 2) / 3
}
