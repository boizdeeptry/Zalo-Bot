package daemon

import (
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"
)

const openCodeTestModel = "opencode/deepseek-v4-flash-free"

func TestOpenCodeSpikeSpecBuildsExactRunAndDiscoveryPolicy(t *testing.T) {
	root := t.TempDir()
	binary := filepath.Join(root, "opencode.exe")
	run, err := newOpenCodeSpikeSpec(root, binary, openCodeTestModel)
	if err != nil {
		t.Fatal(err)
	}
	wantRun := []string{
		"--pure", "--log-level", "ERROR", "run", "--format", "json",
		"--model", openCodeTestModel, "--agent", "agentdc-synthetic", "--dir", filepath.Join(root, "work"),
	}
	if !slices.Equal(run.Args, wantRun) {
		t.Fatalf("run args = %#v; want %#v", run.Args, wantRun)
	}
	if run.Binary != binary || run.WorkDir != filepath.Join(root, "work") {
		t.Fatalf("run paths = binary %q workdir %q", run.Binary, run.WorkDir)
	}
	if run.Stdin != syntheticOpenCodePrompt {
		t.Fatalf("run stdin = %q", run.Stdin)
	}
	joined := strings.Join(run.Args, "\x00")
	for _, banned := range []string{
		syntheticOpenCodePrompt, "--auto", "--yolo", "--dangerously-skip-permissions",
		"--session", "--continue", "--fork", "--share", "--file", "--command", "--attach", "--refresh",
	} {
		if strings.Contains(joined, banned) {
			t.Fatalf("run argv contains banned value %q", banned)
		}
	}

	discovery, err := newOpenCodeModelDiscoverySpec(root, binary)
	if err != nil {
		t.Fatal(err)
	}
	wantDiscovery := []string{"--pure", "--log-level", "ERROR", "models", "opencode"}
	if !slices.Equal(discovery.Args, wantDiscovery) {
		t.Fatalf("discovery args = %#v; want %#v", discovery.Args, wantDiscovery)
	}
	if discovery.Stdin != "" || discovery.Binary != binary || discovery.WorkDir != filepath.Join(root, "work") {
		t.Fatalf("discovery spec = %#v", discovery)
	}
	if !slices.Equal(run.Env, discovery.Env) {
		t.Fatal("run and discovery environments differ")
	}
}

func TestOpenCodeSpikeSpecUsesOnlyCanonicalOwnedEnvironment(t *testing.T) {
	root := t.TempDir()
	binary := filepath.Join(root, "opencode.exe")
	t.Setenv("PATH", "C:\\foreign-bin")
	t.Setenv("HTTP_PROXY", "http://foreign.invalid")
	t.Setenv("HTTPS_PROXY", "http://foreign.invalid")
	t.Setenv("NPM_TOKEN", "npm-secret")
	t.Setenv("NPM_CONFIG_REGISTRY", "https://foreign.invalid")
	t.Setenv("OPENAI_API_KEY", "openai-secret")
	t.Setenv("ANTHROPIC_API_KEY", "anthropic-secret")
	t.Setenv("OPENCODE_CONFIG_DIR", "C:\\foreign-config")
	t.Setenv("OPENCODE_SERVER_PASSWORD", "server-secret")
	t.Setenv("XDG_DATA_HOME", "C:\\foreign-data")

	spec, err := newOpenCodeSpikeSpec(root, binary, openCodeTestModel)
	if err != nil {
		t.Fatal(err)
	}
	got := envMap(t, spec.Env)
	want := map[string]string{
		"XDG_DATA_HOME":                    filepath.Join(root, "data"),
		"XDG_CONFIG_HOME":                  filepath.Join(root, "config"),
		"XDG_CACHE_HOME":                   filepath.Join(root, "cache"),
		"XDG_STATE_HOME":                   filepath.Join(root, "state"),
		"HOME":                             filepath.Join(root, "home"),
		"USERPROFILE":                      filepath.Join(root, "home"),
		"TEMP":                             filepath.Join(root, "temp"),
		"TMP":                              filepath.Join(root, "temp"),
		"OPENCODE_CONFIG_CONTENT":          canonicalOpenCodeConfig(t),
		"OPENCODE_DB":                      ":memory:",
		"OPENCODE_PURE":                    "1",
		"OPENCODE_PERMISSION":              `{"*":"deny"}`,
		"OPENCODE_LOG_LEVEL":               "ERROR",
		"OPENCODE_DISABLE_PROJECT_CONFIG":  "1",
		"OPENCODE_DISABLE_EXTERNAL_SKILLS": "1",
		"OPENCODE_DISABLE_CLAUDE_CODE":     "1",
		"OPENCODE_DISABLE_LSP_DOWNLOAD":    "1",
		"OPENCODE_DISABLE_AUTOUPDATE":      "1",
		"OPENCODE_DISABLE_MODELS_FETCH":    "1",
		"OPENCODE_DISABLE_AUTOCOMPACT":     "1",
		"OPENCODE_AUTO_SHARE":              "0",
		"NPM_CONFIG_OFFLINE":               "true",
		"NPM_CONFIG_CACHE":                 filepath.Join(root, "npm-cache"),
		"NPM_CONFIG_PREFIX":                filepath.Join(root, "npm-prefix"),
		"NPM_CONFIG_AUDIT":                 "false",
		"NPM_CONFIG_FUND":                  "false",
		"NPM_CONFIG_UPDATE_NOTIFIER":       "false",
	}
	if runtime.GOOS == "windows" {
		want["SYSTEMROOT"] = os.Getenv("SYSTEMROOT")
	}
	if len(got) != len(want) {
		t.Fatalf("env keys = %v; want %v", sortedMapKeys(got), sortedMapKeys(want))
	}
	for key, value := range want {
		if got[key] != value {
			t.Errorf("env %s = %q; want %q", key, got[key], value)
		}
	}
	for _, forbidden := range []string{
		"PATH", "HTTP_PROXY", "HTTPS_PROXY", "NPM_TOKEN", "NPM_CONFIG_REGISTRY",
		"OPENAI_API_KEY", "ANTHROPIC_API_KEY", "OPENCODE_CONFIG_DIR", "OPENCODE_SERVER_PASSWORD",
	} {
		if _, exists := got[forbidden]; exists {
			t.Errorf("ambient key %s leaked", forbidden)
		}
	}
}

func TestOpenCodeSpikeSpecRejectsUnsafeInputs(t *testing.T) {
	root := t.TempDir()
	binary := filepath.Join(root, "opencode.exe")
	volumeRoot := filepath.VolumeName(root) + string(filepath.Separator)
	uncleanRoot := root + string(filepath.Separator) + "child" + string(filepath.Separator) + ".."
	uncleanBinary := root + string(filepath.Separator) + "bin" + string(filepath.Separator) + ".." + string(filepath.Separator) + "opencode.exe"
	tests := []struct {
		name, root, binary, model string
	}{
		{"empty root", "", binary, openCodeTestModel},
		{"relative root", "relative", binary, openCodeTestModel},
		{"unclean root", uncleanRoot, binary, openCodeTestModel},
		{"volume root", volumeRoot, binary, openCodeTestModel},
		{"nul root", root + "\x00x", binary, openCodeTestModel},
		{"empty binary", root, "", openCodeTestModel},
		{"relative binary", root, "opencode.exe", openCodeTestModel},
		{"unclean binary", root, uncleanBinary, openCodeTestModel},
		{"nul binary", root, binary + "\x00x", openCodeTestModel},
		{"empty model", root, binary, ""},
		{"wrong namespace", root, binary, "openai/model-free"},
		{"wrong suffix", root, binary, "opencode/model"},
		{"extra slash", root, binary, "opencode/vendor/model-free"},
		{"uppercase", root, binary, "opencode/Model-free"},
		{"space", root, binary, "opencode/model free"},
		{"control", root, binary, "opencode/model\n-free"},
		{"unsafe separator", root, binary, "opencode/model@x-free"},
		{"oversized", root, binary, "opencode/" + strings.Repeat("a", 250) + "-free"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := newOpenCodeSpikeSpec(test.root, test.binary, test.model)
			if !errors.Is(err, ErrOpenCodeUnsafeSpec) {
				t.Fatalf("error = %v; want unsafe spec", err)
			}
		})
	}
}

func TestParseOpenCodeSpikeFreeModelsSortsDeduplicatesAndSelects(t *testing.T) {
	models, err := parseOpenCodeFreeModels(strings.NewReader(
		"opencode/mimo-v2.5-free\r\nopencode/deepseek-v4-flash-free\n" +
			"opencode/mimo-v2.5-free\n"))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"opencode/deepseek-v4-flash-free", "opencode/mimo-v2.5-free"}
	if !slices.Equal(models, want) {
		t.Fatalf("models = %#v; want %#v", models, want)
	}
	selected, err := selectOpenCodeSpikeModel(models)
	if err != nil || selected != openCodeTestModel {
		t.Fatalf("selected = %q, %v", selected, err)
	}
	selected, err = selectOpenCodeSpikeModel([]string{"opencode/zeta-free", "opencode/alpha-free"})
	if err != nil || selected != "opencode/alpha-free" {
		t.Fatalf("fallback = %q, %v", selected, err)
	}
	if _, err = selectOpenCodeSpikeModel([]string{"other/unsafe-free"}); !errors.Is(err, ErrOpenCodeInvalidProtocol) {
		t.Fatalf("unsafe selector error = %v", err)
	}
	_, err = selectOpenCodeSpikeModel(nil)
	if !errors.Is(err, ErrOpenCodeNoSafeModel) {
		t.Fatalf("empty selector error = %v", err)
	}
}

func TestParseOpenCodeSpikeFreeModelsFailsClosed(t *testing.T) {
	invalid := []string{
		"other/unsafe-free", "opencode/not-paid", "opencode/vendor/model-free", "opencode/UPPER-free",
		"opencode/white space-free", " opencode/model-free", "opencode/model-free ", "\t",
		"opencode/model@x-free", "opencode/mô-hình-free", "opencode/model\x00-free",
		"opencode/" + strings.Repeat("a", 250) + "-free",
	}
	for _, line := range invalid {
		t.Run(fmt.Sprintf("%q", line), func(t *testing.T) {
			_, err := parseOpenCodeFreeModels(strings.NewReader("opencode/good-free\n" + line + "\n"))
			if !errors.Is(err, ErrOpenCodeInvalidProtocol) {
				t.Fatalf("error = %v", err)
			}
		})
	}
	badUTF8 := append([]byte("opencode/good-free\n"), 0xff, '\n')
	if _, err := parseOpenCodeFreeModels(strings.NewReader(string(badUTF8))); !errors.Is(err, ErrOpenCodeInvalidProtocol) {
		t.Fatalf("invalid UTF-8 error = %v", err)
	}
	if _, err := parseOpenCodeFreeModels(strings.NewReader(strings.Repeat("a", (1<<20)+1))); !errors.Is(err, ErrOpenCodeOutputTooLarge) {
		t.Fatalf("line cap error = %v", err)
	}
	validLine := "opencode/alpha-free\n"
	if _, err := parseOpenCodeFreeModels(strings.NewReader(strings.Repeat(validLine, (1<<20)/len(validLine)+1))); !errors.Is(err, ErrOpenCodeOutputTooLarge) {
		t.Fatalf("aggregate cap error = %v", err)
	}
	if _, err := parseOpenCodeFreeModels(&openCodeErrorReader{payload: []byte("opencode/good-free\n"), err: errors.New("reader secret")}); err == nil || strings.Contains(err.Error(), "reader secret") {
		t.Fatalf("reader error was not sanitized: %v", err)
	}
}

func TestParseOpenCodeSpikeNDJSONAcceptsExactV11818Stream(t *testing.T) {
	stream := strings.Join([]string{
		openCodeStepStart("ses_1", "msg_1", "prt_1", 100, `,"snapshot":"snap"`),
		openCodeText("ses_1", "msg_1", "prt_2", 101, "O", `,"synthetic":false,"ignored":false,"metadata":{"vendor":{"values":[1,2]}}`),
		openCodeText("ses_1", "msg_1", "prt_3", 101, "K", ""),
		openCodeStepFinish("ses_1", "msg_1", "prt_4", 102, false, `,"snapshot":"snap"`),
	}, "\n") + "\n"
	got, err := parseOpenCodeSpikeNDJSON(strings.NewReader(stream))
	if err != nil {
		t.Fatal(err)
	}
	want := openCodeSpikeResult{SessionID: "ses_1", Text: "OK"}
	if got != want {
		t.Fatalf("result = %#v; want %#v", got, want)
	}
}

func TestParseOpenCodeSpikeNDJSONAcceptsFiniteV11818Counters(t *testing.T) {
	stream := strings.NewReplacer(
		`"timestamp":2`, `"timestamp":1.5`,
		`"cost":0`, `"cost":0.5`,
		`"total":2`, `"total":2.5`,
		`"input":1`, `"input":1.5`,
		`"output":1`, `"output":1.25`,
		`"reasoning":0`, `"reasoning":0.5`,
		`"read":0`, `"read":0.25`,
		`"write":0`, `"write":0.75`,
	).Replace(validOpenCodeStream("ses_finite", "OK"))
	got, err := parseOpenCodeSpikeNDJSON(strings.NewReader(stream))
	if err != nil {
		t.Fatal(err)
	}
	if got != (openCodeSpikeResult{SessionID: "ses_finite", Text: "OK"}) {
		t.Fatalf("result = %#v", got)
	}
}

func TestParseOpenCodeSpikeNDJSONRejectsDuplicateAndAliasedKeys(t *testing.T) {
	valid := validOpenCodeStream("ses_duplicate", "OK")
	tests := map[string]string{
		"top duplicate":          strings.Replace(valid, `"type":"step_start"`, `"type":"step_start","type":"step_start"`, 1),
		"top escaped duplicate":  strings.Replace(valid, `"type":"step_start"`, `"type":"step_start","t\u0079pe":"step_start"`, 1),
		"top case alias":         strings.Replace(valid, `"type":"step_start"`, `"type":"step_start","Type":"step_start"`, 1),
		"part session duplicate": strings.Replace(valid, `"sessionID":"ses_duplicate","messageID"`, `"sessionID":"ses_duplicate","sessionID":"ses_duplicate","messageID"`, 1),
		"time end duplicate":     strings.Replace(valid, `"end":2`, `"end":2,"end":2`, 1),
		"token input duplicate":  strings.Replace(valid, `"input":1`, `"input":1,"input":1`, 1),
		"cache read duplicate":   strings.Replace(valid, `"read":0`, `"read":0,"read":0`, 1),
		"metadata alias":         strings.Replace(valid, `"text":"OK","time"`, `"text":"OK","metadata":{"key":1,"Key":2},"time"`, 1),
	}
	for name, stream := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := parseOpenCodeSpikeNDJSON(strings.NewReader(stream))
			if !errors.Is(err, ErrOpenCodeInvalidProtocol) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestParseOpenCodeSpikeNDJSONRejectsInvalidOrder(t *testing.T) {
	start := openCodeStepStart("ses_order", "msg", "prt_1", 1, "")
	text := openCodeText("ses_order", "msg", "prt_2", 2, "OK", "")
	finish := openCodeStepFinish("ses_order", "msg", "prt_3", 3, true, "")
	tests := map[string]string{
		"empty":               "",
		"blank line":          start + "\n\n" + text + "\n" + finish + "\n",
		"text first":          text + "\n" + start + "\n" + finish + "\n",
		"finish first":        finish + "\n",
		"duplicate start":     start + "\n" + start + "\n" + text + "\n" + finish + "\n",
		"finish without text": start + "\n" + finish + "\n",
		"missing finish":      start + "\n" + text + "\n",
		"second finish":       start + "\n" + text + "\n" + finish + "\n" + finish + "\n",
		"event after finish":  start + "\n" + text + "\n" + finish + "\n" + text + "\n",
		"two values one line": start + " " + text + "\n" + finish + "\n",
	}
	for name, stream := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := parseOpenCodeSpikeNDJSON(strings.NewReader(stream))
			if !errors.Is(err, ErrOpenCodeInvalidProtocol) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestParseOpenCodeSpikeNDJSONRejectsIdentityAndPartDrift(t *testing.T) {
	base := validOpenCodeStream("ses_identity", "OK")
	tests := map[string]string{
		"blank envelope session":   strings.Replace(base, `"sessionID":"ses_identity"`, `"sessionID":""`, 1),
		"control envelope session": strings.Replace(base, `"sessionID":"ses_identity"`, `"sessionID":"ses_\nidentity"`, 1),
		"mixed envelope session":   strings.Replace(base, `"type":"text","timestamp":2,"sessionID":"ses_identity"`, `"type":"text","timestamp":2,"sessionID":"ses_other"`, 1),
		"nested session mismatch":  strings.Replace(base, `"id":"prt_text","sessionID":"ses_identity"`, `"id":"prt_text","sessionID":"ses_other"`, 1),
		"missing part id":          strings.Replace(base, `"id":"prt_text",`, "", 1),
		"missing message id":       strings.Replace(base, `,"messageID":"msg_1"`, "", 1),
		"message drift":            strings.Replace(base, `"id":"prt_text","sessionID":"ses_identity","messageID":"msg_1"`, `"id":"prt_text","sessionID":"ses_identity","messageID":"msg_2"`, 1),
		"duplicate part id":        strings.Replace(base, `"id":"prt_finish"`, `"id":"prt_text"`, 1),
		"part type mismatch":       strings.Replace(base, `"id":"prt_text","sessionID":"ses_identity","messageID":"msg_1","type":"text"`, `"id":"prt_text","sessionID":"ses_identity","messageID":"msg_1","type":"step-start"`, 1),
		"invalid session prefix":   strings.ReplaceAll(base, `"sessionID":"ses_identity"`, `"sessionID":"bad_identity"`),
		"invalid message prefix":   strings.ReplaceAll(base, `"messageID":"msg_1"`, `"messageID":"bad_1"`),
		"invalid part prefix":      strings.Replace(base, `"id":"prt_text"`, `"id":"part_text"`, 1),
		"timestamp decreases":      strings.Replace(base, `"type":"step_finish","timestamp":3`, `"type":"step_finish","timestamp":1`, 1),
	}
	for name, stream := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := parseOpenCodeSpikeNDJSON(strings.NewReader(stream))
			if !errors.Is(err, ErrOpenCodeInvalidProtocol) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestParseOpenCodeSpikeNDJSONRejectsUnsafeEventsAndIncompleteParts(t *testing.T) {
	base := validOpenCodeStream("ses_parts", "OK")
	tests := map[string]string{
		"tool event":            strings.Replace(base, `"type":"text"`, `"type":"tool_use"`, 1),
		"reasoning event":       strings.Replace(base, `"type":"text"`, `"type":"reasoning"`, 1),
		"error event":           strings.Replace(base, `"type":"text"`, `"type":"error"`, 1),
		"unknown event":         strings.Replace(base, `"type":"text"`, `"type":"future"`, 1),
		"missing timestamp":     strings.Replace(base, `,"timestamp":2`, "", 1),
		"wrong timestamp type":  strings.Replace(base, `"timestamp":2`, `"timestamp":"2"`, 1),
		"negative timestamp":    strings.Replace(base, `"timestamp":2`, `"timestamp":-1`, 1),
		"text missing time":     strings.Replace(base, `,"time":{"start":1,"end":2}`, "", 1),
		"text missing end":      strings.Replace(base, `,"end":2`, "", 1),
		"text end before start": strings.Replace(base, `"start":1,"end":2`, `"start":2,"end":1`, 1),
		"empty text":            strings.Replace(base, `"text":"OK"`, `"text":""`, 1),
		"finish missing reason": strings.Replace(base, `,"reason":"stop"`, "", 1),
		"finish empty reason":   strings.Replace(base, `"reason":"stop"`, `"reason":""`, 1),
		"finish missing cost":   strings.Replace(base, `,"cost":0`, "", 1),
		"finish negative cost":  strings.Replace(base, `"cost":0`, `"cost":-1`, 1),
		"finish missing tokens": strings.Replace(base, `,"tokens":{"total":2,"input":1,"output":1,"reasoning":0,"cache":{"read":0,"write":0}}`, "", 1),
		"finish missing input":  strings.Replace(base, `"input":1,`, "", 1),
		"finish missing cache":  strings.Replace(base, `,"cache":{"read":0,"write":0}`, "", 1),
		"finish negative token": strings.Replace(base, `"output":1`, `"output":-1`, 1),
		"unknown top field":     strings.Replace(base, `"type":"step_start"`, `"type":"step_start","secret":"x"`, 1),
		"unknown part field":    strings.Replace(base, `"type":"text","text"`, `"type":"text","secret":"x","text"`, 1),
		"unknown time field":    strings.Replace(base, `"start":1,"end":2`, `"start":1,"end":2,"secret":1`, 1),
		"unknown tokens field":  strings.Replace(base, `"reasoning":0,"cache"`, `"reasoning":0,"secret":1,"cache"`, 1),
		"metadata null":         strings.Replace(base, `"text":"OK","time"`, `"text":"OK","metadata":null,"time"`, 1),
		"metadata array":        strings.Replace(base, `"text":"OK","time"`, `"text":"OK","metadata":[],"time"`, 1),
		"metadata scalar":       strings.Replace(base, `"text":"OK","time"`, `"text":"OK","metadata":"x","time"`, 1),
	}
	for name, stream := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := parseOpenCodeSpikeNDJSON(strings.NewReader(stream))
			if !errors.Is(err, ErrOpenCodeInvalidProtocol) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestParseOpenCodeSpikeNDJSONEnforcesBoundsAndSanitizesErrors(t *testing.T) {
	invalidUTF8 := append([]byte(openCodeStepStart("ses_utf8", "msg", "prt_1", 1, "")+"\n"), 0xff, '\n')
	if _, err := parseOpenCodeSpikeNDJSON(strings.NewReader(string(invalidUTF8))); !errors.Is(err, ErrOpenCodeInvalidProtocol) {
		t.Fatalf("invalid UTF-8 error = %v", err)
	}
	large := validOpenCodeStream("ses_large", strings.Repeat("x", (1<<20)+1))
	if _, err := parseOpenCodeSpikeNDJSON(strings.NewReader(large)); !errors.Is(err, ErrOpenCodeOutputTooLarge) {
		t.Fatalf("large output error = %v", err)
	}
	var many strings.Builder
	many.WriteString(openCodeStepStart("ses_many", "msg", "prt_0", 1, "") + "\n")
	for i := 0; i < 1025; i++ {
		many.WriteString(openCodeText("ses_many", "msg", fmt.Sprintf("prt_%d", i+1), int64(i+2), "x", "") + "\n")
	}
	many.WriteString(openCodeStepFinish("ses_many", "msg", "prt_final", 2000, true, "") + "\n")
	if _, err := parseOpenCodeSpikeNDJSON(strings.NewReader(many.String())); !errors.Is(err, ErrOpenCodeOutputTooLarge) {
		t.Fatalf("event cap error = %v", err)
	}
	deep := strings.Repeat(`{"a":`, 18) + `1` + strings.Repeat(`}`, 18)
	deepStream := strings.Replace(validOpenCodeStream("ses_depth", "OK"), `"text":"OK","time"`, `"text":"OK","metadata":`+deep+`,"time"`, 1)
	if _, err := parseOpenCodeSpikeNDJSON(strings.NewReader(deepStream)); !errors.Is(err, ErrOpenCodeInvalidProtocol) {
		t.Fatalf("depth error = %v", err)
	}
	tokenValues := strings.Repeat("0,", openCodeJSONTokenLimit) + "0"
	tokenStream := strings.Replace(validOpenCodeStream("ses_tokens", "OK"), `"text":"OK","time"`, `"text":"OK","metadata":{"values":[`+tokenValues+`]},"time"`, 1)
	if _, err := parseOpenCodeSpikeNDJSON(strings.NewReader(tokenStream)); !errors.Is(err, ErrOpenCodeInvalidProtocol) {
		t.Fatalf("token cap error = %v", err)
	}
	secret := "ses_DO_NOT_ECHO_SECRET_SESSION"
	_, err := parseOpenCodeSpikeNDJSON(&openCodeErrorReader{payload: []byte(openCodeStepStart(secret, "msg", "prt_secret", 1, "") + "\n"), err: errors.New("reader raw secret")})
	if err == nil || strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), "reader raw secret") {
		t.Fatalf("reader error leaked input: %v", err)
	}
}

func envMap(t *testing.T, env []string) map[string]string {
	t.Helper()
	result := make(map[string]string, len(env))
	for _, item := range env {
		key, value, ok := strings.Cut(item, "=")
		if !ok || key == "" {
			t.Fatalf("malformed env entry %q", item)
		}
		upper := strings.ToUpper(key)
		if _, exists := result[upper]; exists {
			t.Fatalf("duplicate env key %q", key)
		}
		result[upper] = value
	}
	return result
}

func sortedMapKeys(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys
}

func canonicalOpenCodeConfig(t *testing.T) string {
	t.Helper()
	return `{"enabled_providers":["opencode"],"share":"disabled","snapshot":false,"autoupdate":false,"formatter":false,"lsp":false,"plugin":[],"mcp":{},"subagent_depth":0,"permission":{"*":"deny"},"agent":{"agentdc-synthetic":{"mode":"primary","steps":1,"permission":{"*":"deny"}}}}`
}

func validOpenCodeStream(sessionID, text string) string {
	return strings.Join([]string{
		openCodeStepStart(sessionID, "msg_1", "prt_start", 1, ""),
		openCodeText(sessionID, "msg_1", "prt_text", 2, text, ""),
		openCodeStepFinish(sessionID, "msg_1", "prt_finish", 3, true, ""),
	}, "\n") + "\n"
}

func openCodeStepStart(sessionID, messageID, partID string, timestamp int64, optional string) string {
	return fmt.Sprintf(`{"type":"step_start","timestamp":%d,"sessionID":%q,"part":{"id":%q,"sessionID":%q,"messageID":%q,"type":"step-start"%s}}`,
		timestamp, sessionID, partID, sessionID, messageID, optional)
}

func openCodeText(sessionID, messageID, partID string, timestamp int64, text, optional string) string {
	return fmt.Sprintf(`{"type":"text","timestamp":%d,"sessionID":%q,"part":{"id":%q,"sessionID":%q,"messageID":%q,"type":"text","text":%q,"time":{"start":1,"end":2}%s}}`,
		timestamp, sessionID, partID, sessionID, messageID, text, optional)
}

func openCodeStepFinish(sessionID, messageID, partID string, timestamp int64, total bool, optional string) string {
	totalField := ""
	if total {
		totalField = `"total":2,`
	}
	return fmt.Sprintf(`{"type":"step_finish","timestamp":%d,"sessionID":%q,"part":{"id":%q,"sessionID":%q,"messageID":%q,"type":"step-finish","reason":"stop","cost":0,"tokens":{%s"input":1,"output":1,"reasoning":0,"cache":{"read":0,"write":0}}%s}}`,
		timestamp, sessionID, partID, sessionID, messageID, totalField, optional)
}

type openCodeErrorReader struct {
	payload []byte
	err     error
	done    bool
}

func (r *openCodeErrorReader) Read(p []byte) (int, error) {
	if !r.done && len(r.payload) > 0 {
		r.done = true
		return copy(p, r.payload), nil
	}
	return 0, r.err
}

func TestOpenCodeTestFixturesAreValidUTF8(t *testing.T) {
	if !utf8.ValidString(validOpenCodeStream("ses_fixture", "OK")) {
		t.Fatal("fixture is invalid UTF-8")
	}
}

func TestOpenCodeSyntheticDescriptorIsPrivateAndNeverAdvertised(t *testing.T) {
	descriptor, exists := appSyntheticRuntimeDescriptors["opencode"]
	if !exists {
		t.Fatal("synthetic OpenCode descriptor is missing")
	}
	if descriptor.Kind != "opencode" || descriptor.Version != "1.18.18" {
		t.Fatalf("descriptor identity = %+v", descriptor)
	}
	if descriptor.Advertised || !descriptor.SyntheticOnly {
		t.Fatalf("unsafe synthetic descriptor flags: %+v", descriptor)
	}
	for _, option := range appProviderOptions() {
		if option.Kind == descriptor.Kind {
			t.Fatal("synthetic descriptor leaked into production options")
		}
	}
	if _, exists := productionAppProviderRuntimeRegistry().registration(descriptor.Kind); exists {
		t.Fatal("synthetic descriptor leaked into production capabilities")
	}
	if _, exists := cliDescriptors[descriptor.Kind]; exists {
		t.Fatal("synthetic descriptor leaked into production runners")
	}
}

func TestOpenCodeSyntheticDescriptorPinsReviewedWindowsRelease(t *testing.T) {
	if got := strings.ToUpper(hex.EncodeToString(openCodePinnedSHA256[:])); got != "B6EFA9EB3EE1D5C25438F3CBA03C471E5D8661A6121ABC47F209DB3B3F05F415" {
		t.Fatalf("pinned SHA-256 = %s", got)
	}
	if openCodePinnedVersion != "1.18.18" || openCodeExpectedSignerSubject != "Anomaly Innovations, Inc" {
		t.Fatalf("pinned release identity = %q / %q", openCodePinnedVersion, openCodeExpectedSignerSubject)
	}
}
