package daemon

import (
	"io/fs"
	"net/http"
	"strings"
	"testing"
)

func TestAppPortalShellAssetsAreEmbedded(t *testing.T) {
	for _, name := range []string{
		"index.html",
		"app-main.js",
		"core/router.js",
		"core/api.js",
		"core/state.js",
		"core/ui.js",
		"pages/agents.js",
		"pages/knowledge.js",
		"pages/models.js",
		"pages/overview.js",
	} {
		if _, err := fs.ReadFile(assetFS, name); err != nil {
			t.Errorf("read Portal asset %q: %v", name, err)
		}
	}

	index, err := fs.ReadFile(assetFS, "index.html")
	if err != nil {
		t.Fatal(err)
	}
	body := string(index)
	for _, want := range []string{
		`data-portal-nav`,
		`data-portal-content`,
		`href="/zalo"`,
		`<script type="module" src="/assets/app-main.js"></script>`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("Portal index missing %q", want)
		}
	}
	if strings.Contains(strings.ToLower(body), "chưa có") {
		t.Error("Portal shell exposes a legacy 'chưa có' placeholder")
	}
}

func TestAppPackageServesManagementAssetsAndZalo(t *testing.T) {
	t.Setenv(envPortalOpen, "1")
	ts, _, _ := newTestServer(t, nil)
	for _, path := range []string{"/", "/assets/core/router.js", "/zalo"} {
		response := rawGet(t, ts, path)
		if response.StatusCode != http.StatusOK {
			t.Errorf("GET %s = %d; want 200", path, response.StatusCode)
		}
	}
}
