package daemon

import (
	"slices"
	"testing"
)

func TestCLIRegistryHasThreeVendors(t *testing.T) {
	got := make([]string, 0, len(cliDescriptors))
	for kind := range cliDescriptors {
		got = append(got, kind)
	}
	slices.Sort(got)
	want := []string{"claude-code", "codex", "gemini-cli"}
	if !slices.Equal(got, want) {
		t.Fatalf("cliDescriptors kinds = %v; want %v", got, want)
	}
	for kind, d := range cliDescriptors {
		if d.kind != kind {
			t.Errorf("map key %q != d.kind %q", kind, d.kind)
		}
		if d.readOnlyArgs == nil {
			t.Errorf("%s: readOnlyArgs nil — mọi vendor phải có cờ chỉ-đọc", kind)
		}
		if len(d.modelSeeds) == 0 {
			t.Errorf("%s: modelSeeds rỗng — Discover trả danh sách tĩnh", kind)
		}
	}
}
