package daemon

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"agentdc/internal/providercatalog"
	"agentdc/internal/store"
)

const (
	runtimeProviderHTTPPrivateCredential = "private-provider-credential-canary"
	runtimeProviderHTTPPrivateRegistry   = "private-opencode-registry-canary"
)

func TestRuntimeProviderHTTPRegisteredRouteUsesExactContext(t *testing.T) {
	env := newOnboardingRouteTestEnv(t)
	registry := runtimeLiveRegistry(t, nil)
	runtimeLiveSeedAccountRuntime(
		t,
		env,
		"future-cli",
		"Future CLI",
		"future-model",
		true,
	)

	response := runtimeLiveServe(
		t,
		runtimeLiveContext(t, env, registry),
		http.MethodGet,
		"/llm/providers",
		"",
	)
	if response.Code != http.StatusOK {
		t.Fatalf("GET /llm/providers status=%d body=%s", response.Code, response.Body.String())
	}
	body := runtimeProviderHTTPObject(t, response)
	if connected, ok := body["hasConnectedProvider"].(bool); !ok || !connected {
		t.Fatalf("hasConnectedProvider=%v; want true from the synthetic context", body["hasConnectedProvider"])
	}

	options := runtimeProviderHTTPArray(t, body, "provider_options")
	futureOption := runtimeProviderHTTPEntryByKind(t, options, "future-cli")
	if mode := futureOption["connection_mode"]; mode != "account" {
		t.Fatalf("future-cli option connection_mode=%v; want account", mode)
	}

	providers := runtimeProviderHTTPArray(t, body, "providers")
	futureBody := runtimeProviderHTTPEntryByKind(t, providers, "future-cli")
	if mode := futureBody["connection_mode"]; mode != "account" {
		t.Fatalf("future-cli body connection_mode=%v; want account", mode)
	}
}

func TestRuntimeProviderHTTPSafeOptionsAndProviderBodies(t *testing.T) {
	env := newOnboardingRouteTestEnv(t)
	registry := runtimeLiveRegistry(t, nil)
	runtimeLiveSeedAccountRuntime(t, env, "codex", "Codex", "codex-model", false)
	runtimeLiveSeedAccountRuntime(t, env, "future-cli", "Future CLI", "future-model", false)
	runtimeLiveSeedAccountRuntime(t, env, "gemini-cli", "Gemini CLI", "gemini-model", true)
	if err := env.a.st.CreateLLMProvider(store.LLMProvider{
		ID: "openai", Name: "OpenAI fixture", Kind: "openai", Enabled: true,
	}); err != nil {
		t.Fatalf("CreateLLMProvider(openai) = %v", err)
	}
	privateCipher := []byte(runtimeProviderHTTPPrivateCredential)
	if err := env.a.st.SetLLMCredentialCipher("openai", privateCipher); err != nil {
		t.Fatalf("SetLLMCredentialCipher(openai) = %v", err)
	}

	response := runtimeLiveServe(
		t,
		runtimeLiveContext(t, env, registry),
		http.MethodGet,
		"/llm/providers",
		"",
	)
	if response.Code != http.StatusOK {
		t.Fatalf("GET /llm/providers status=%d body=%s", response.Code, response.Body.String())
	}
	body := runtimeProviderHTTPObject(t, response)
	options := runtimeProviderHTTPArray(t, body, "provider_options")
	wantOptions := runtimeProviderHTTPExpectedOptions(t)
	if !reflect.DeepEqual(options, wantOptions) {
		t.Fatalf("provider_options mismatch\n got: %#v\nwant: %#v", options, wantOptions)
	}

	optionByKind := make(map[string]map[string]any, len(options))
	for _, entry := range options {
		option := runtimeProviderHTTPMap(t, entry, "provider option")
		kind, ok := option["kind"].(string)
		if !ok || kind == "" {
			t.Fatalf("provider option has invalid kind: %#v", option)
		}
		optionByKind[kind] = option
	}

	providers := runtimeProviderHTTPArray(t, body, "providers")
	seenBodies := make(map[string]bool, len(providers))
	for _, entry := range providers {
		provider := runtimeProviderHTTPMap(t, entry, "provider body")
		kind, ok := provider["kind"].(string)
		if !ok || kind == "" {
			t.Fatalf("provider body has invalid kind: %#v", provider)
		}
		option, ok := optionByKind[kind]
		if !ok {
			t.Errorf("provider body %q has no matching safe option", kind)
			continue
		}
		if got, ok := provider["connection_mode"].(string); !ok || got != option["connection_mode"] {
			t.Errorf(
				"provider body %q connection_mode=%v; want matching option mode %v",
				kind,
				provider["connection_mode"],
				option["connection_mode"],
			)
		}
		seenBodies[kind] = true
	}
	for _, kind := range []string{"codex", "future-cli", "openai", "gemini-cli"} {
		if !seenBodies[kind] {
			t.Errorf("provider body %q is missing", kind)
		}
	}

	runtimeProviderHTTPAssertNoPrivateKeys(t, body)
	runtimeProviderHTTPAssertNoPrivateValues(
		t,
		body,
		env.dataDir,
		runtimeProviderHTTPPrivateCredential,
		base64.StdEncoding.EncodeToString(privateCipher),
	)
}

func TestRuntimeProviderHTTPInvalidRegistryFailsClosed(t *testing.T) {
	env := newOnboardingRouteTestEnv(t)
	ctx := appRuntimeContext{
		api: env.a, registry: appProviderRuntimeRegistry{}, connect: env.a.newConnectManager(),
	}
	response := httptest.NewRecorder()
	ctx.handleLLMProviderList(
		response,
		httptest.NewRequest(http.MethodGet, "/llm/providers", nil),
	)

	runtimeProviderHTTPAssertGenericInternalError(t, response, "future-cli", env.dataDir)
}

func TestRuntimeProviderHTTPOpenCodeRegistryFailsClosed(t *testing.T) {
	env := newOnboardingRouteTestEnv(t)
	registry := runtimeProviderHTTPPoisonedOpenCodeRegistry(t)
	runtimeLiveSeedAccountRuntime(t, env, "future-cli", "Future CLI", "future-model", true)

	response := runtimeLiveServe(
		t,
		runtimeLiveContext(t, env, registry),
		http.MethodGet,
		"/llm/providers",
		"",
	)
	runtimeProviderHTTPAssertGenericInternalError(
		t,
		response,
		"opencode",
		"future-cli",
		runtimeProviderHTTPPrivateRegistry,
		env.dataDir,
	)
}

func runtimeProviderHTTPExpectedOptions(t *testing.T) []any {
	t.Helper()
	const expected = `[
		{"kind":"codex","display_name":"Codex","description":"Kết nối tài khoản ChatGPT/Codex trên máy này","group":"subscription","connectable":true,"connection_mode":"account","execution_mode":"proxy","visible":true,"prefix":"cx","theme_color":"#0f7a63","beta":false,"ui_order":10},
		{"kind":"future-cli","display_name":"Future CLI","description":"Kết nối runtime thử nghiệm an toàn","group":"subscription","connectable":true,"connection_mode":"account","execution_mode":"local","visible":true,"prefix":"fc","theme_color":"#334455","beta":false,"ui_order":15},
		{"kind":"claude-code","display_name":"Claude Code","description":"Kết nối tài khoản Claude Code trên máy này","group":"subscription","connectable":true,"connection_mode":"account","execution_mode":"official_cli","visible":true,"prefix":"cc","theme_color":"#c8613b","beta":false,"ui_order":20},
		{"kind":"openai","display_name":"OpenAI","description":"Kết nối OpenAI bằng API key","group":"apikey","connectable":false,"connection_mode":"credential","execution_mode":"api","visible":true,"prefix":"oa","theme_color":"#10a37f","beta":false,"ui_order":30},
		{"kind":"anthropic","display_name":"Anthropic","description":"Kết nối Anthropic bằng API key","group":"apikey","connectable":false,"connection_mode":"credential","execution_mode":"api","visible":true,"prefix":"an","theme_color":"#c8613b","beta":false,"ui_order":40},
		{"kind":"gemini","display_name":"Google Gemini","description":"Kết nối Google Gemini bằng API key","group":"apikey","connectable":false,"connection_mode":"credential","execution_mode":"api","visible":true,"prefix":"gm","theme_color":"#3f6ff5","beta":false,"ui_order":50},
		{"kind":"openrouter","display_name":"OpenRouter","description":"Kết nối OpenRouter bằng API key","group":"apikey","connectable":false,"connection_mode":"credential","execution_mode":"api","visible":true,"prefix":"or","theme_color":"#5b5ef0","beta":false,"ui_order":60},
		{"kind":"gemini-cli","display_name":"Gemini CLI","description":"Runtime CLI tương thích cho định tuyến đã lưu","group":"subscription","connectable":false,"connection_mode":"none","execution_mode":"official_cli","visible":false,"prefix":"gc","theme_color":"#3f6ff5","beta":false,"ui_order":70}
	]`
	var options []any
	if err := json.Unmarshal([]byte(expected), &options); err != nil {
		t.Fatalf("decode expected provider options: %v", err)
	}
	return options
}

func runtimeProviderHTTPPoisonedOpenCodeRegistry(t *testing.T) appProviderRuntimeRegistry {
	t.Helper()
	registry := runtimeLiveRegistry(t, nil)

	opencode := appRuntimeFutureRegistration()
	opencode.Metadata.Kind = "opencode"
	opencode.Metadata.DisplayName = "OpenCode"
	opencode.Metadata.Description = runtimeProviderHTTPPrivateRegistry
	opencode.Metadata.Prefix = "oc"
	opencode.Metadata.UIOrder = 200
	opencode.Metadata.CatalogRank = 200

	options := append(registry.catalog.Options(), providercatalog.Option{
		Kind: "opencode", DisplayName: "OpenCode", Description: runtimeProviderHTTPPrivateRegistry,
		Advertised: true, RouteRank: 200,
	})
	poisonedCatalog, err := providercatalog.New(options)
	if err != nil {
		t.Fatalf("build poisoned OpenCode catalog: %v", err)
	}

	byKind := make(map[string]appProviderRuntimeRegistration, len(registry.byKind)+1)
	for kind, registration := range registry.byKind {
		byKind[kind] = registration
	}
	byKind["opencode"] = opencode
	management := append([]appProviderManagementOption(nil), registry.management...)
	management = append(management, opencode.Metadata.managementOption())

	registry.catalog = poisonedCatalog
	registry.byKind = byKind
	registry.management = management
	return registry
}

func runtimeProviderHTTPObject(
	t *testing.T,
	response *httptest.ResponseRecorder,
) map[string]any {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode HTTP JSON %q: %v", response.Body.String(), err)
	}
	return body
}

func runtimeProviderHTTPArray(t *testing.T, object map[string]any, field string) []any {
	t.Helper()
	array, ok := object[field].([]any)
	if !ok {
		t.Fatalf("response field %q=%#v; want an array", field, object[field])
	}
	return array
}

func runtimeProviderHTTPMap(t *testing.T, value any, label string) map[string]any {
	t.Helper()
	object, ok := value.(map[string]any)
	if !ok {
		t.Fatalf("%s=%#v; want an object", label, value)
	}
	return object
}

func runtimeProviderHTTPEntryByKind(t *testing.T, entries []any, wantKind string) map[string]any {
	t.Helper()
	for _, entry := range entries {
		object := runtimeProviderHTTPMap(t, entry, "kind entry")
		if object["kind"] == wantKind {
			return object
		}
	}
	t.Fatalf("kind %q is absent from %#v", wantKind, entries)
	return nil
}

func runtimeProviderHTTPAssertNoPrivateKeys(t *testing.T, value any) {
	t.Helper()
	forbidden := map[string]struct{}{
		"connect": {}, "ensureaccountprovider": {}, "seedconnectedmodels": {},
		"selectonboardingmodel": {}, "newonboardingmember": {}, "newadapter": {},
		"newlocalattachmentrun": {}, "terminal": {}, "newstructuredsession": {},
		"callback": {}, "callbacks": {}, "config": {}, "configdir": {},
		"configpath": {}, "credential": {}, "credentialcipher": {}, "apikey": {},
		"secret": {}, "command": {}, "args": {}, "env": {}, "environment": {},
	}
	var walk func(any, string)
	walk = func(current any, path string) {
		switch typed := current.(type) {
		case map[string]any:
			for key, nested := range typed {
				normalized := strings.ToLower(strings.NewReplacer("_", "", "-", "").Replace(key))
				if _, blocked := forbidden[normalized]; blocked {
					t.Errorf("private field %q exposed at %s", key, path)
				}
				walk(nested, path+"."+key)
			}
		case []any:
			for _, nested := range typed {
				walk(nested, path+"[]")
			}
		}
	}
	walk(value, "response")
}

func runtimeProviderHTTPAssertNoPrivateValues(t *testing.T, value any, forbidden ...string) {
	t.Helper()
	var walk func(any, string)
	walk = func(current any, path string) {
		switch typed := current.(type) {
		case string:
			for _, private := range forbidden {
				if private != "" && strings.Contains(typed, private) {
					t.Errorf("private value %q exposed at %s", private, path)
				}
			}
		case map[string]any:
			for key, nested := range typed {
				walk(nested, path+"."+key)
			}
		case []any:
			for _, nested := range typed {
				walk(nested, path+"[]")
			}
		}
	}
	walk(value, "response")
}

func runtimeProviderHTTPAssertGenericInternalError(
	t *testing.T,
	response *httptest.ResponseRecorder,
	private ...string,
) {
	t.Helper()
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d; want 500 with no partial response; body=%s", response.Code, response.Body.String())
	}
	body := runtimeProviderHTTPObject(t, response)
	if len(body) != 1 {
		t.Fatalf("500 response fields=%#v; want only error", body)
	}
	errorValue, ok := body["error"]
	if !ok {
		t.Fatalf("500 response has no error envelope: %#v", body)
	}
	errorBody := runtimeProviderHTTPMap(t, errorValue, "error")
	if len(errorBody) != 2 || errorBody["code"] != "INTERNAL" {
		t.Fatalf("error=%#v; want generic INTERNAL code/message only", errorBody)
	}
	message, ok := errorBody["message"].(string)
	if !ok || strings.TrimSpace(message) == "" {
		t.Fatalf("generic error message=%#v; want non-empty string", errorBody["message"])
	}
	runtimeProviderHTTPAssertNoPrivateValues(t, body, private...)
}
