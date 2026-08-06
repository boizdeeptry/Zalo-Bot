package daemon

import (
	"errors"
	"io/fs"
	"net/http"
	"strings"
	"testing"
)

func TestAppPortalShellAssetsAreEmbedded(t *testing.T) {
	for _, name := range []string{
		"index.html",
		"app-main.js",
		"portal.css",
		"core/router.js",
		"core/api.js",
		"core/state.js",
		"core/ui.js",
		"core/shell.js",
		"pages/agents.js",
		"pages/knowledge.js",
		"pages/models.js",
		"pages/providers.js",
		"pages/roadmap.js",
	} {
		if _, err := fs.ReadFile(assetFS, name); err != nil {
			t.Errorf("read Portal asset %q: %v", name, err)
		}
	}
	if _, err := fs.ReadFile(assetFS, "pages/overview.js"); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("read removed Portal overview asset: got %v; want fs.ErrNotExist", err)
	}

	index, err := fs.ReadFile(assetFS, "index.html")
	if err != nil {
		t.Fatal(err)
	}
	body := string(index)
	for _, want := range []string{
		`id="rail"`,
		`id="main"`,
		`<link rel="stylesheet" href="/assets/app.css">`,
		`<link rel="stylesheet" href="/assets/portal.css">`,
		`<script type="module" src="/assets/app-main.js"></script>`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("Portal index missing %q", want)
		}
	}
}

func TestAppPackageServesManagementAssetsAndZalo(t *testing.T) {
	t.Setenv(envPortalOpen, "1")
	ts, _, _ := newTestServer(t, nil)
	for _, path := range []string{
		"/",
		"/assets/app-main.js",
		"/assets/portal.css",
		"/assets/app.css",
		"/assets/zalo.css",
		"/assets/modal.css",
		"/zalo",
	} {
		response := rawGet(t, ts, path)
		if response.StatusCode != http.StatusOK {
			t.Errorf("GET %s = %d; want 200", path, response.StatusCode)
		}
	}
}
