package daemon

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

const (
	syntheticOpenCodePrompt = "Chỉ trả lời đúng một từ: OK"

	openCodePinnedVersion         = "1.18.18"
	openCodeExpectedSignerSubject = "Anomaly Innovations, Inc"
	openCodePreferredModel        = "opencode/deepseek-v4-flash-free"
	openCodeOutputLimit           = 1 << 20
	openCodeEventLimit            = 1024
	openCodeJSONDepthLimit        = 16
	openCodeJSONTokenLimit        = 4096
	openCodeIdentifierMax         = 256
	openCodeModelMax              = 256

	openCodeCanonicalConfig = `{"enabled_providers":["opencode"],"share":"disabled","snapshot":false,"autoupdate":false,"formatter":false,"lsp":false,"plugin":[],"mcp":{},"subagent_depth":0,"permission":{"*":"deny"},"agent":{"agentdc-synthetic":{"mode":"primary","steps":1,"permission":{"*":"deny"}}}}`
)

var (
	ErrOpenCodeContainmentUnavailable = errors.New("OpenCode OS containment unavailable")
	ErrOpenCodeUnsafeSpec             = errors.New("unsafe OpenCode spike specification")
	ErrOpenCodeNoSafeModel            = errors.New("no safe OpenCode model")
	ErrOpenCodeInvalidProtocol        = errors.New("invalid OpenCode protocol")
	ErrOpenCodeOutputTooLarge         = errors.New("OpenCode output too large")
	ErrOpenCodeBinaryRejected         = errors.New("OpenCode synthetic binary rejected")
)

var openCodePinnedSHA256 = [32]byte{
	0xB6, 0xEF, 0xA9, 0xEB, 0x3E, 0xE1, 0xD5, 0xC2,
	0x54, 0x38, 0xF3, 0xCB, 0xA0, 0x3C, 0x47, 0x1E,
	0x5D, 0x86, 0x61, 0xA6, 0x12, 0x1A, 0xBC, 0x47,
	0xF2, 0x09, 0xDB, 0x3B, 0x3F, 0x05, 0xF4, 0x15,
}

// appSyntheticRuntimeDescriptor is intentionally data-only and is never read
// by production provider catalogs, capabilities, routers or auth drivers.
type appSyntheticRuntimeDescriptor struct {
	Kind          string
	Version       string
	Advertised    bool
	SyntheticOnly bool
}

type openCodeSmokeRootLock interface {
	io.Closer
	entries() ([]string, error)
	remove() error
}

var appSyntheticRuntimeDescriptors = map[string]appSyntheticRuntimeDescriptor{
	"opencode": {
		Kind:          "opencode",
		Version:       openCodePinnedVersion,
		Advertised:    false,
		SyntheticOnly: true,
	},
}

type openCodeSpikeSpec struct {
	Binary  string
	WorkDir string
	Stdin   string
	Args    []string
	Env     []string
}

type openCodeSpikeResult struct {
	SessionID string
	Text      string
}

func newOpenCodeSpikeSpec(root, binary, model string) (openCodeSpikeSpec, error) {
	base, err := newOpenCodeBaseSpec(root, binary)
	if err != nil {
		return openCodeSpikeSpec{}, err
	}
	if !validOpenCodeModel(model) {
		return openCodeSpikeSpec{}, openCodeUnsafeSpecError("model")
	}
	base.Stdin = syntheticOpenCodePrompt
	base.Args = []string{
		"--pure", "--log-level", "ERROR", "run", "--format", "json",
		"--model", model, "--agent", "agentdc-synthetic", "--dir", base.WorkDir,
	}
	return base, nil
}

func newOpenCodeModelDiscoverySpec(root, binary string) (openCodeSpikeSpec, error) {
	base, err := newOpenCodeBaseSpec(root, binary)
	if err != nil {
		return openCodeSpikeSpec{}, err
	}
	base.Args = []string{"--pure", "--log-level", "ERROR", "models", "opencode"}
	return base, nil
}

func newOpenCodeBaseSpec(root, binary string) (openCodeSpikeSpec, error) {
	if !validOpenCodeOwnedRoot(root) {
		return openCodeSpikeSpec{}, openCodeUnsafeSpecError("root")
	}
	if !validOpenCodeAbsolutePath(binary, false) {
		return openCodeSpikeSpec{}, openCodeUnsafeSpecError("binary")
	}

	env := []string{
		"XDG_DATA_HOME=" + filepath.Join(root, "data"),
		"XDG_CONFIG_HOME=" + filepath.Join(root, "config"),
		"XDG_CACHE_HOME=" + filepath.Join(root, "cache"),
		"XDG_STATE_HOME=" + filepath.Join(root, "state"),
		"HOME=" + filepath.Join(root, "home"),
		"USERPROFILE=" + filepath.Join(root, "home"),
		"TEMP=" + filepath.Join(root, "temp"),
		"TMP=" + filepath.Join(root, "temp"),
		"OPENCODE_CONFIG_CONTENT=" + openCodeCanonicalConfig,
		"OPENCODE_DB=:memory:",
		"OPENCODE_PURE=1",
		`OPENCODE_PERMISSION={"*":"deny"}`,
		"OPENCODE_LOG_LEVEL=ERROR",
		"OPENCODE_DISABLE_PROJECT_CONFIG=1",
		"OPENCODE_DISABLE_EXTERNAL_SKILLS=1",
		"OPENCODE_DISABLE_CLAUDE_CODE=1",
		"OPENCODE_DISABLE_LSP_DOWNLOAD=1",
		"OPENCODE_DISABLE_AUTOUPDATE=1",
		"OPENCODE_DISABLE_MODELS_FETCH=1",
		"OPENCODE_DISABLE_AUTOCOMPACT=1",
		"OPENCODE_AUTO_SHARE=0",
		"NPM_CONFIG_OFFLINE=true",
		"NPM_CONFIG_CACHE=" + filepath.Join(root, "npm-cache"),
		"NPM_CONFIG_PREFIX=" + filepath.Join(root, "npm-prefix"),
		"NPM_CONFIG_AUDIT=false",
		"NPM_CONFIG_FUND=false",
		"NPM_CONFIG_UPDATE_NOTIFIER=false",
	}
	if runtime.GOOS == "windows" {
		systemRoot := os.Getenv("SYSTEMROOT")
		if !validOpenCodeAbsolutePath(systemRoot, false) {
			return openCodeSpikeSpec{}, openCodeUnsafeSpecError("system root")
		}
		env = append(env, "SYSTEMROOT="+systemRoot)
	}
	return openCodeSpikeSpec{
		Binary:  binary,
		WorkDir: filepath.Join(root, "work"),
		Env:     env,
	}, nil
}

func validOpenCodeOwnedRoot(path string) bool {
	return validOpenCodeAbsolutePath(path, true)
}

func validOpenCodeAbsolutePath(path string, rejectRoot bool) bool {
	if path == "" || strings.IndexByte(path, 0) >= 0 || !filepath.IsAbs(path) {
		return false
	}
	clean := filepath.Clean(path)
	if clean != path {
		return false
	}
	if rejectRoot && filepath.Dir(clean) == clean {
		return false
	}
	return true
}

func validOpenCodeModel(model string) bool {
	const prefix = "opencode/"
	const suffix = "-free"
	if len(model) == 0 || len(model) > openCodeModelMax || !strings.HasPrefix(model, prefix) || !strings.HasSuffix(model, suffix) {
		return false
	}
	name := model[len(prefix) : len(model)-len(suffix)]
	if name == "" {
		return false
	}
	separator := false
	for index := 0; index < len(name); index++ {
		character := name[index]
		if character >= 'a' && character <= 'z' || character >= '0' && character <= '9' {
			separator = false
			continue
		}
		if character != '.' && character != '_' && character != '-' || index == 0 || index == len(name)-1 || separator {
			return false
		}
		separator = true
	}
	return true
}

func parseOpenCodeFreeModels(reader io.Reader) ([]string, error) {
	models := make(map[string]struct{})
	err := readBoundedOpenCodeLines(reader, func(line []byte) error {
		if len(line) == 0 {
			return nil
		}
		if !utf8.Valid(line) || !validOpenCodeModel(string(line)) {
			return openCodeProtocolError()
		}
		models[string(line)] = struct{}{}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(models) == 0 {
		return nil, openCodeNoModelError()
	}
	result := make([]string, 0, len(models))
	for model := range models {
		result = append(result, model)
	}
	sort.Strings(result)
	return result, nil
}

func selectOpenCodeSpikeModel(models []string) (string, error) {
	if len(models) == 0 {
		return "", openCodeNoModelError()
	}
	copyOfModels := append([]string(nil), models...)
	for _, model := range copyOfModels {
		if !validOpenCodeModel(model) {
			return "", openCodeProtocolError()
		}
	}
	sort.Strings(copyOfModels)
	for _, model := range copyOfModels {
		if model == openCodePreferredModel {
			return model, nil
		}
	}
	return copyOfModels[0], nil
}

type parsedOpenCodeEvent struct {
	kind      string
	timestamp float64
	sessionID string
	messageID string
	partID    string
	text      string
}

func parseOpenCodeSpikeNDJSON(reader io.Reader) (openCodeSpikeResult, error) {
	var result openCodeSpikeResult
	var text strings.Builder
	partIDs := make(map[string]struct{})
	state := 0
	events := 0
	textEvents := 0
	var lastTimestamp float64
	var messageID string

	err := readBoundedOpenCodeLines(reader, func(line []byte) error {
		if len(line) == 0 || !utf8.Valid(line) {
			return openCodeProtocolError()
		}
		events++
		if events > openCodeEventLimit {
			return openCodeOutputLimitError()
		}
		event, err := parseOpenCodeEvent(line)
		if err != nil {
			return err
		}
		if events > 1 && event.timestamp < lastTimestamp {
			return openCodeProtocolError()
		}
		lastTimestamp = event.timestamp
		if result.SessionID == "" {
			result.SessionID = event.sessionID
		} else if result.SessionID != event.sessionID {
			return openCodeProtocolError()
		}
		if messageID == "" {
			messageID = event.messageID
		} else if messageID != event.messageID {
			return openCodeProtocolError()
		}
		if _, exists := partIDs[event.partID]; exists {
			return openCodeProtocolError()
		}
		partIDs[event.partID] = struct{}{}

		switch state {
		case 0:
			if event.kind != "step_start" {
				return openCodeProtocolError()
			}
			state = 1
		case 1:
			switch event.kind {
			case "text":
				if text.Len() > openCodeOutputLimit-len(event.text) {
					return openCodeOutputLimitError()
				}
				text.WriteString(event.text)
				textEvents++
			case "step_finish":
				if textEvents == 0 {
					return openCodeProtocolError()
				}
				state = 2
			default:
				return openCodeProtocolError()
			}
		default:
			return openCodeProtocolError()
		}
		return nil
	})
	if err != nil {
		return openCodeSpikeResult{}, err
	}
	if state != 2 || textEvents == 0 || result.SessionID == "" {
		return openCodeSpikeResult{}, openCodeProtocolError()
	}
	result.Text = text.String()
	return result, nil
}

func parseOpenCodeEvent(line []byte) (parsedOpenCodeEvent, error) {
	if err := validateOpenCodeJSONStructure(line); err != nil {
		return parsedOpenCodeEvent{}, err
	}
	top, err := openCodeStrictObject(line,
		[]string{"type", "timestamp", "sessionID", "part"},
		[]string{"type", "timestamp", "sessionID", "part"})
	if err != nil {
		return parsedOpenCodeEvent{}, err
	}
	kind, ok := openCodeString(top["type"])
	if !ok || kind != "step_start" && kind != "text" && kind != "step_finish" {
		return parsedOpenCodeEvent{}, openCodeProtocolError()
	}
	timestamp, ok := openCodeNonnegativeFloat(top["timestamp"])
	if !ok || math.IsInf(timestamp, 0) || math.IsNaN(timestamp) {
		return parsedOpenCodeEvent{}, openCodeProtocolError()
	}
	sessionID, ok := openCodeString(top["sessionID"])
	if !ok || !validOpenCodePrefixedIdentifier(sessionID, "ses") {
		return parsedOpenCodeEvent{}, openCodeProtocolError()
	}
	part, err := parseOpenCodePart(kind, top["part"])
	if err != nil {
		return parsedOpenCodeEvent{}, err
	}
	if part.sessionID != sessionID {
		return parsedOpenCodeEvent{}, openCodeProtocolError()
	}
	part.kind = kind
	part.timestamp = timestamp
	return part, nil
}

func parseOpenCodePart(kind string, raw json.RawMessage) (parsedOpenCodeEvent, error) {
	common := []string{"id", "sessionID", "messageID", "type"}
	allowed := append([]string(nil), common...)
	required := append([]string(nil), common...)
	switch kind {
	case "step_start":
		allowed = append(allowed, "snapshot")
	case "text":
		allowed = append(allowed, "text", "time", "synthetic", "ignored", "metadata")
		required = append(required, "text", "time")
	case "step_finish":
		allowed = append(allowed, "reason", "cost", "tokens", "snapshot")
		required = append(required, "reason", "cost", "tokens")
	default:
		return parsedOpenCodeEvent{}, openCodeProtocolError()
	}
	part, err := openCodeStrictObject(raw, allowed, required)
	if err != nil {
		return parsedOpenCodeEvent{}, err
	}
	partID, partIDOK := openCodeString(part["id"])
	sessionID, sessionIDOK := openCodeString(part["sessionID"])
	messageID, messageIDOK := openCodeString(part["messageID"])
	partType, partTypeOK := openCodeString(part["type"])
	if !partIDOK || !sessionIDOK || !messageIDOK || !partTypeOK ||
		!validOpenCodePrefixedIdentifier(partID, "prt") ||
		!validOpenCodePrefixedIdentifier(sessionID, "ses") ||
		!validOpenCodePrefixedIdentifier(messageID, "msg") ||
		partType != strings.ReplaceAll(kind, "_", "-") {
		return parsedOpenCodeEvent{}, openCodeProtocolError()
	}
	parsed := parsedOpenCodeEvent{sessionID: sessionID, messageID: messageID, partID: partID}

	if snapshot, exists := part["snapshot"]; exists {
		value, ok := openCodeString(snapshot)
		if !ok || len(value) > openCodeOutputLimit {
			return parsedOpenCodeEvent{}, openCodeProtocolError()
		}
	}
	switch kind {
	case "text":
		value, ok := openCodeString(part["text"])
		if !ok || value == "" || !utf8.ValidString(value) {
			return parsedOpenCodeEvent{}, openCodeProtocolError()
		}
		if err := validateOpenCodeTextTime(part["time"]); err != nil {
			return parsedOpenCodeEvent{}, err
		}
		for _, key := range []string{"synthetic", "ignored"} {
			if rawValue, exists := part[key]; exists {
				if _, ok := openCodeBoolean(rawValue); !ok {
					return parsedOpenCodeEvent{}, openCodeProtocolError()
				}
			}
		}
		if metadata, exists := part["metadata"]; exists {
			var record map[string]json.RawMessage
			if err := json.Unmarshal(metadata, &record); err != nil || record == nil {
				return parsedOpenCodeEvent{}, openCodeProtocolError()
			}
		}
		parsed.text = value
	case "step_finish":
		reason, ok := openCodeString(part["reason"])
		if !ok || !validOpenCodeReason(reason) {
			return parsedOpenCodeEvent{}, openCodeProtocolError()
		}
		cost, ok := openCodeNonnegativeFloat(part["cost"])
		if !ok || math.IsInf(cost, 0) || math.IsNaN(cost) {
			return parsedOpenCodeEvent{}, openCodeProtocolError()
		}
		if err := validateOpenCodeTokens(part["tokens"]); err != nil {
			return parsedOpenCodeEvent{}, err
		}
	}
	return parsed, nil
}

func validateOpenCodeTextTime(raw json.RawMessage) error {
	values, err := openCodeStrictObject(raw, []string{"start", "end"}, []string{"start", "end"})
	if err != nil {
		return err
	}
	start, startOK := openCodeNonnegativeInteger(values["start"])
	end, endOK := openCodeNonnegativeInteger(values["end"])
	if !startOK || !endOK || end < start {
		return openCodeProtocolError()
	}
	return nil
}

func validateOpenCodeTokens(raw json.RawMessage) error {
	values, err := openCodeStrictObject(raw,
		[]string{"total", "input", "output", "reasoning", "cache"},
		[]string{"input", "output", "reasoning", "cache"})
	if err != nil {
		return err
	}
	for _, key := range []string{"input", "output", "reasoning"} {
		if _, ok := openCodeNonnegativeFloat(values[key]); !ok {
			return openCodeProtocolError()
		}
	}
	if total, exists := values["total"]; exists {
		if _, ok := openCodeNonnegativeFloat(total); !ok {
			return openCodeProtocolError()
		}
	}
	cache, err := openCodeStrictObject(values["cache"], []string{"read", "write"}, []string{"read", "write"})
	if err != nil {
		return err
	}
	for _, key := range []string{"read", "write"} {
		if _, ok := openCodeNonnegativeFloat(cache[key]); !ok {
			return openCodeProtocolError()
		}
	}
	return nil
}

func validateOpenCodeJSONStructure(raw []byte) error {
	if !utf8.Valid(raw) {
		return openCodeProtocolError()
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	budget := 0
	if err := walkOpenCodeJSON(decoder, 0, &budget); err != nil {
		return openCodeProtocolError()
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return openCodeProtocolError()
	}
	return nil
}

func walkOpenCodeJSON(decoder *json.Decoder, depth int, budget *int) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	*budget++
	if *budget > openCodeJSONTokenLimit {
		return errors.New("JSON token limit")
	}
	delimiter, isDelimiter := token.(json.Delim)
	if !isDelimiter {
		return nil
	}
	if depth >= openCodeJSONDepthLimit {
		return errors.New("JSON depth limit")
	}
	switch delimiter {
	case '{':
		keys := make([]string, 0)
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			*budget++
			if *budget > openCodeJSONTokenLimit {
				return errors.New("JSON token limit")
			}
			key, ok := keyToken.(string)
			if !ok {
				return errors.New("non-string object key")
			}
			for _, prior := range keys {
				if strings.EqualFold(prior, key) {
					return errors.New("duplicate object key")
				}
			}
			keys = append(keys, key)
			if err := walkOpenCodeJSON(decoder, depth+1, budget); err != nil {
				return err
			}
		}
		end, err := decoder.Token()
		if err != nil || end != json.Delim('}') {
			return errors.New("unterminated object")
		}
		*budget++
	case '[':
		for decoder.More() {
			if err := walkOpenCodeJSON(decoder, depth+1, budget); err != nil {
				return err
			}
		}
		end, err := decoder.Token()
		if err != nil || end != json.Delim(']') {
			return errors.New("unterminated array")
		}
		*budget++
	default:
		return errors.New("unexpected delimiter")
	}
	if *budget > openCodeJSONTokenLimit {
		return errors.New("JSON token limit")
	}
	return nil
}

func openCodeStrictObject(raw []byte, allowed, required []string) (map[string]json.RawMessage, error) {
	var values map[string]json.RawMessage
	if err := json.Unmarshal(raw, &values); err != nil || values == nil {
		return nil, openCodeProtocolError()
	}
	for key := range values {
		if !containsOpenCodeKey(allowed, key) {
			return nil, openCodeProtocolError()
		}
	}
	for _, key := range required {
		if _, exists := values[key]; !exists {
			return nil, openCodeProtocolError()
		}
	}
	return values, nil
}

func containsOpenCodeKey(keys []string, candidate string) bool {
	for _, key := range keys {
		if key == candidate {
			return true
		}
	}
	return false
}

func openCodeString(raw json.RawMessage) (string, bool) {
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", false
	}
	return value, true
}

func openCodeBoolean(raw json.RawMessage) (bool, bool) {
	var value bool
	if err := json.Unmarshal(raw, &value); err != nil {
		return false, false
	}
	return value, true
}

func openCodeNonnegativeInteger(raw json.RawMessage) (int64, bool) {
	value, err := strconv.ParseInt(strings.TrimSpace(string(raw)), 10, 64)
	return value, err == nil && value >= 0
}

func openCodeNonnegativeFloat(raw json.RawMessage) (float64, bool) {
	value, err := strconv.ParseFloat(strings.TrimSpace(string(raw)), 64)
	return value, err == nil && value >= 0 && !math.IsInf(value, 0) && !math.IsNaN(value)
}

func validOpenCodePrefixedIdentifier(value, prefix string) bool {
	if value == "" || len(value) > openCodeIdentifierMax || !strings.HasPrefix(value, prefix) {
		return false
	}
	for index := 0; index < len(value); index++ {
		character := value[index]
		if character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' ||
			character >= '0' && character <= '9' || character == '_' || character == '-' || character == '.' {
			continue
		}
		return false
	}
	return true
}

func validOpenCodeReason(value string) bool {
	if value == "" || len(value) > openCodeIdentifierMax || strings.TrimSpace(value) != value {
		return false
	}
	for index := 0; index < len(value); index++ {
		if value[index] < 0x20 || value[index] > 0x7e {
			return false
		}
	}
	return true
}

func readBoundedOpenCodeLines(reader io.Reader, consume func([]byte) error) error {
	buffered := bufio.NewReaderSize(reader, 64<<10)
	line := make([]byte, 0, 64<<10)
	total := 0
	for {
		fragment, readErr := buffered.ReadSlice('\n')
		if len(fragment) > openCodeOutputLimit-total || len(line) > openCodeOutputLimit-len(fragment) {
			return openCodeOutputLimitError()
		}
		total += len(fragment)
		line = append(line, fragment...)
		if errors.Is(readErr, bufio.ErrBufferFull) {
			continue
		}
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			return openCodeProtocolError()
		}
		if len(line) == 0 && errors.Is(readErr, io.EOF) {
			break
		}
		if len(line) > 0 && line[len(line)-1] == '\n' {
			line = line[:len(line)-1]
			if len(line) > 0 && line[len(line)-1] == '\r' {
				line = line[:len(line)-1]
			}
		}
		if err := consume(line); err != nil {
			return err
		}
		line = line[:0]
		if errors.Is(readErr, io.EOF) {
			break
		}
	}
	return nil
}

func openCodeUnsafeSpecError(category string) error {
	return fmt.Errorf("%w: invalid %s", ErrOpenCodeUnsafeSpec, category)
}

func openCodeNoModelError() error {
	return fmt.Errorf("%w: discovery returned no usable model", ErrOpenCodeNoSafeModel)
}

func openCodeProtocolError() error {
	return fmt.Errorf("%w: rejected output", ErrOpenCodeInvalidProtocol)
}

func openCodeOutputLimitError() error {
	return fmt.Errorf("%w: output limits exceeded", ErrOpenCodeOutputTooLarge)
}

func openCodeBinaryRejectedError(category string) error {
	return fmt.Errorf("%w: %s", ErrOpenCodeBinaryRejected, category)
}
