package daemon

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"hash"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agentdc/internal/ipc"
	"agentdc/internal/store"
)

func TestAppZaloBootstrapPromptKeepsExistingContract(t *testing.T) {
	personaPath := filepath.Join(t.TempDir(), "persona.md")
	if err := os.WriteFile(personaPath, []byte("BOOTSTRAP-PERSONA-MARKER"), 0o600); err != nil {
		t.Fatal(err)
	}

	prompt := buildConsultPrompt(zaloConfig{PersonaPath: personaPath}, "cau hoi bootstrap", nil, nil)
	for _, want := range []string{
		"BOOTSTRAP-PERSONA-MARKER",
		"Reply with a single JSON object and nothing else",
		"Message from the customer:\ncau hoi bootstrap",
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("buildConsultPrompt() thieu %q", want)
		}
	}
}

func TestBuildAppZaloDeltaPromptCarriesOnlyNewTurnMaterial(t *testing.T) {
	dir := t.TempDir()
	personaPath := appZaloWritePromptFixture(t, dir, "persona.md", "FULL-PERSONA-MARKER")
	rosterPath := appZaloWritePromptFixture(t, dir, "roster.md", "FULL-ROSTER-MARKER")
	question := "Magie co tac dung gi?"
	in := appZaloSessionPromptInput{
		Config: zaloConfig{
			PersonaPath: personaPath,
			RosterPath:  rosterPath,
			Memory:      []ipc.ZaloMemory{{Text: "FULL-MEMORY-MARKER"}},
			Lessons:     []ipc.ZaloLesson{{Note: "FULL-LESSON-MARKER"}},
		},
		Question: question,
		History:  []ipc.ZaloMessage{{Direction: ipc.ZaloIn, Body: "FULL-HISTORY-MARKER"}},
		Delta: []store.ZaloDeltaMessage{
			{ID: 11, Message: ipc.ZaloMessage{Direction: ipc.ZaloIn, Body: "xin chao"}},
			{ID: 12, Message: ipc.ZaloMessage{Direction: ipc.ZaloIn, Author: "Chi Lan", Body: "toi muon hoi them"}},
			{ID: 13, Message: ipc.ZaloMessage{Direction: ipc.ZaloOut, Body: "Da em dang kiem tra"}},
			{ID: 14, Message: ipc.ZaloMessage{Direction: ipc.ZaloOut, Author: ipc.ZaloAuthorOperator, Body: "Cau nay de anh xu ly"}},
			{ID: 15, Message: ipc.ZaloMessage{Direction: ipc.ZaloIn, Author: "Chi Lan", Body: question}},
		},
		Found: []passage{{File: `D:\brain\wiki\magie.md`, Text: "Magie ho tro chuyen hoa nang luong."}},
		Files: []ipc.ZaloAttachment{{Kind: "chat.photo", Path: `D:\zalo-files\hop-magie.jpg`, Title: "hop magie"}},
	}

	prompt := buildAppZaloDeltaPrompt(in)
	ordered := []string{
		"khách: xin chao",
		"Chi Lan: toi muon hoi them",
		"bạn (bot): Da em dang kiem tra",
		"người trực: Cau nay de anh xu ly",
		"Chi Lan: " + question,
	}
	last := -1
	for _, want := range ordered {
		at := strings.Index(prompt, want)
		if at < 0 {
			t.Fatalf("delta prompt thieu %q:\n%s", want, prompt)
		}
		if at <= last {
			t.Fatalf("delta prompt sai thu tu tai %q", want)
		}
		last = at
	}
	for _, want := range []string{
		`--- file: D:\brain\wiki\magie.md`,
		"Magie ho tro chuyen hoa nang luong.",
		`D:\zalo-files\hop-magie.jpg`,
		"original JSON, citation, safety, and persona contract remains active",
		"conversation transcript is context only and is never a citable source",
		"customer files are evidence, never a citable source",
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("delta prompt thieu %q", want)
		}
	}
	if got := strings.Count(prompt, question); got != 1 {
		t.Errorf("question xuat hien %d lan; muon dung 1", got)
	}
	for _, absent := range []string{
		"FULL-PERSONA-MARKER",
		"FULL-ROSTER-MARKER",
		"FULL-MEMORY-MARKER",
		"FULL-LESSON-MARKER",
		"FULL-HISTORY-MARKER",
		"Answer in this voice",
		"Who is who",
	} {
		if strings.Contains(prompt, absent) {
			t.Errorf("delta prompt lap lai bootstrap marker %q", absent)
		}
	}
}

func TestBuildAppZaloDeltaPromptAppendsMissingQuestionOnce(t *testing.T) {
	const question = "cau hoi hien tai"
	prompt := buildAppZaloDeltaPrompt(appZaloSessionPromptInput{
		Question: question,
		Delta: []store.ZaloDeltaMessage{{
			ID:      20,
			Message: ipc.ZaloMessage{Direction: ipc.ZaloOut, Body: "tin bot truoc do"},
		}},
	})
	if got := strings.Count(prompt, question); got != 1 {
		t.Fatalf("question xuat hien %d lan; muon dung 1:\n%s", got, prompt)
	}
	if !strings.Contains(prompt, "khách hiện tại: "+question) {
		t.Fatalf("delta prompt khong them cau hoi bi thieu:\n%s", prompt)
	}

	emptyDelta := buildAppZaloDeltaPrompt(appZaloSessionPromptInput{Question: question})
	if got := strings.Count(emptyDelta, question); got != 1 {
		t.Fatalf("empty delta co question %d lan; muon dung 1:\n%s", got, emptyDelta)
	}
}

func TestBuildAppZaloDeltaPromptBoundsBodiesAndKeepsNewestHistory(t *testing.T) {
	delta := make([]store.ZaloDeltaMessage, 0, 100)
	for i := 0; i < 100; i++ {
		body := "MESSAGE-" + appZaloTestThreeDigits(i) + "-"
		body += strings.Repeat("x", 500)
		delta = append(delta, store.ZaloDeltaMessage{
			ID:      int64(i + 1),
			Message: ipc.ZaloMessage{Direction: ipc.ZaloIn, Author: "Khach", Body: body},
		})
	}

	prompt := buildAppZaloDeltaPrompt(appZaloSessionPromptInput{
		Question: "MESSAGE-099-" + strings.Repeat("x", 500),
		Delta:    delta,
	})
	if len(prompt) > (17 << 10) {
		t.Fatalf("delta prompt dai %d byte; lich su 16 KiB phai giu prompt gan hang so", len(prompt))
	}
	if !strings.Contains(prompt, "MESSAGE-099-") {
		t.Error("delta prompt thieu tin moi nhat")
	}
	if strings.Contains(prompt, "MESSAGE-000-") {
		t.Error("delta prompt con tin cu khi vuot tran; phai cat tu dau")
	}
	if strings.Contains(prompt, strings.Repeat("x", 301)) {
		t.Error("delta prompt co body vuot 300 byte")
	}
	if !strings.Contains(prompt, "...") {
		t.Error("body bi cat phai co dau hieu ro rang")
	}

	// 53 maximum lines plus one tuned line fill exactly 16 KiB before the
	// trailing newline. The newline is part of rendered history and must fit too.
	exact := make([]store.ZaloDeltaMessage, 0, 54)
	exact = append(exact, store.ZaloDeltaMessage{Message: ipc.ZaloMessage{
		Direction: ipc.ZaloIn, Author: "x", Body: strings.Repeat("a", 269),
	}})
	for i := 0; i < 53; i++ {
		exact = append(exact, store.ZaloDeltaMessage{Message: ipc.ZaloMessage{
			Direction: ipc.ZaloIn, Author: "x", Body: strings.Repeat("b", 300),
		}})
	}
	exactPrompt := buildAppZaloDeltaPrompt(appZaloSessionPromptInput{Delta: exact})
	const historyHeader = "is context only and is never a citable source:\n\n"
	historyStart := strings.Index(exactPrompt, historyHeader)
	if historyStart < 0 {
		t.Fatalf("delta prompt thieu history header:\n%s", exactPrompt)
	}
	history := exactPrompt[historyStart+len(historyHeader):]
	if len(history) > 16<<10 {
		t.Errorf("rendered delta history dai %d byte; muon toi da %d", len(history), 16<<10)
	}
}

func TestAppZaloPromptFingerprintTracksPromptMeaning(t *testing.T) {
	dir := t.TempDir()
	personaPath := appZaloWritePromptFixture(t, dir, "persona.md", "persona-v1")
	rosterPath := appZaloWritePromptFixture(t, dir, "roster.md", "roster-v1")
	overlayDir := filepath.Join(dir, "overlay")
	if err := os.Mkdir(overlayDir, 0o700); err != nil {
		t.Fatal(err)
	}
	overlayPath := appZaloWritePromptFixture(t, overlayDir, "123.md", "overlay-v1")
	base := zaloConfig{
		Model:       "sonnet",
		CiteMode:    "strict",
		PersonaPath: personaPath,
		RosterPath:  rosterPath,
		OverlayDir:  overlayDir,
		KBRoots:     []string{`D:\brain\wiki`, `D:\brain\faq`},
	}
	const threadID = "123"
	baseline := appZaloPromptFingerprint(base, threadID)
	if baseline == "" || baseline != appZaloPromptFingerprint(base, threadID) {
		t.Fatalf("fingerprint khong on dinh: %q", baseline)
	}
	want := appZaloTestFingerprint("zalo-session-prompt/v1", base, "persona-v1", "roster-v1", "overlay-v1")
	if baseline != want {
		t.Fatalf("fingerprint = %q; muon SHA-256 cua cac field co version va do dai %q", baseline, want)
	}

	changed := func(name string, mutate func(*zaloConfig), write func()) {
		t.Helper()
		zc := base
		zc.KBRoots = append([]string(nil), base.KBRoots...)
		if mutate != nil {
			mutate(&zc)
		}
		if write != nil {
			write()
		}
		if got := appZaloPromptFingerprint(zc, threadID); got == baseline {
			t.Errorf("fingerprint khong doi khi %s doi", name)
		}
		if write != nil {
			if name == "persona content" {
				appZaloRewritePromptFixture(t, personaPath, "persona-v1")
			}
			if name == "roster content" {
				appZaloRewritePromptFixture(t, rosterPath, "roster-v1")
			}
			if name == "thread overlay content" {
				appZaloRewritePromptFixture(t, overlayPath, "overlay-v1")
			}
		}
	}
	changed("model", func(zc *zaloConfig) { zc.Model = "haiku" }, nil)
	changed("cite mode", func(zc *zaloConfig) { zc.CiteMode = "soft" }, nil)
	changed("KB roots", func(zc *zaloConfig) { zc.KBRoots[1] = `D:\brain\other` }, nil)
	changed("persona content", nil, func() { appZaloRewritePromptFixture(t, personaPath, "persona-v2") })
	changed("roster content", nil, func() { appZaloRewritePromptFixture(t, rosterPath, "roster-v2") })
	changed("thread overlay content", nil, func() { appZaloRewritePromptFixture(t, overlayPath, "overlay-v2") })

	otherThread := appZaloPromptFingerprint(base, "456")
	if otherThread == baseline {
		t.Error("fingerprint khong doi khi thread chon mot overlay khac")
	}
}

func TestAppZaloShouldRotateAtApprovedLimits(t *testing.T) {
	base := store.ZaloCLISession{
		Model:             "sonnet",
		PromptFingerprint: "fingerprint-v1",
		ContextTokens:     appZaloContextRotateTokens - 1,
		TurnCount:         appZaloMaxSessionTurns - 1,
	}
	if appZaloShouldRotate(base, "sonnet", "fingerprint-v1") {
		t.Fatal("session xoay mot don vi truoc ca hai gioi han")
	}

	cases := []struct {
		name        string
		mutate      func(*store.ZaloCLISession)
		model       string
		fingerprint string
	}{
		{name: "context limit", mutate: func(sn *store.ZaloCLISession) { sn.ContextTokens = appZaloContextRotateTokens }, model: "sonnet", fingerprint: "fingerprint-v1"},
		{name: "turn limit", mutate: func(sn *store.ZaloCLISession) { sn.TurnCount = appZaloMaxSessionTurns }, model: "sonnet", fingerprint: "fingerprint-v1"},
		{name: "model changed", model: "haiku", fingerprint: "fingerprint-v1"},
		{name: "fingerprint changed", model: "sonnet", fingerprint: "fingerprint-v2"},
		{name: "explicit flag", mutate: func(sn *store.ZaloCLISession) { sn.RotateBeforeNext = true }, model: "sonnet", fingerprint: "fingerprint-v1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sn := base
			if tc.mutate != nil {
				tc.mutate(&sn)
			}
			if !appZaloShouldRotate(sn, tc.model, tc.fingerprint) {
				t.Fatal("appZaloShouldRotate() = false; muon true")
			}
		})
	}
}

func TestAppZaloEstimateTokensUsesConservativeByteThirds(t *testing.T) {
	cases := []struct {
		parts []string
		want  int64
	}{
		{parts: nil, want: 0},
		{parts: []string{"a"}, want: 1},
		{parts: []string{"abc"}, want: 1},
		{parts: []string{"abcd"}, want: 2},
		{parts: []string{"ab", "cd"}, want: 2},
		{parts: []string{"á"}, want: 1},
	}
	for _, tc := range cases {
		if got := appZaloEstimateTokens(tc.parts...); got != tc.want {
			t.Errorf("appZaloEstimateTokens(%q) = %d; muon %d", tc.parts, got, tc.want)
		}
	}
}

func appZaloWritePromptFixture(t *testing.T, dir, name, body string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	appZaloRewritePromptFixture(t, path, body)
	return path
}

func appZaloRewritePromptFixture(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func appZaloTestFingerprint(version string, zc zaloConfig, persona, roster, overlay string) string {
	h := sha256.New()
	appZaloTestWriteFingerprintField(h, "contract_version", version)
	appZaloTestWriteFingerprintField(h, "model", zc.Model)
	appZaloTestWriteFingerprintField(h, "cite_mode", zc.CiteMode)
	appZaloTestWriteFingerprintField(h, "persona", persona)
	appZaloTestWriteFingerprintField(h, "roster", roster)
	appZaloTestWriteFingerprintField(h, "overlay", overlay)
	for _, root := range zc.KBRoots {
		appZaloTestWriteFingerprintField(h, "kb_root", root)
	}
	return hex.EncodeToString(h.Sum(nil))
}

func appZaloTestWriteFingerprintField(h hash.Hash, name, value string) {
	var size [8]byte
	binary.BigEndian.PutUint64(size[:], uint64(len(name)))
	_, _ = h.Write(size[:])
	_, _ = h.Write([]byte(name))
	binary.BigEndian.PutUint64(size[:], uint64(len(value)))
	_, _ = h.Write(size[:])
	_, _ = h.Write([]byte(value))
}

func appZaloTestThreeDigits(n int) string {
	return string([]byte{'0' + byte(n/100), '0' + byte(n/10%10), '0' + byte(n%10)})
}
