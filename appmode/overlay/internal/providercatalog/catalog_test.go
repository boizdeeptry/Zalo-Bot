package providercatalog

import (
	"errors"
	"slices"
	"testing"
)

func TestProviderCatalogProductionOptionsAreOrderedAndDefensiveCopies(t *testing.T) {
	original := options
	options[0], options[1] = options[1], options[0]
	t.Cleanup(func() { options = original })

	first := Options()
	if len(first) != 2 {
		t.Fatalf("Options() length = %d; want 2", len(first))
	}
	seenRanks := make(map[int]string, len(first))
	seenKinds := make(map[string]bool, len(first))
	for index, option := range first {
		if option.Kind == "" || option.DisplayName == "" || option.Description == "" {
			t.Fatalf("unsafe incomplete catalog option: %+v", option)
		}
		if !option.Advertised {
			t.Fatalf("production option %q is not advertised", option.Kind)
		}
		if previous, exists := seenRanks[option.RouteRank]; exists {
			t.Fatalf("route rank %d is shared by %q and %q", option.RouteRank, previous, option.Kind)
		}
		seenRanks[option.RouteRank] = option.Kind
		seenKinds[option.Kind] = true
		if index > 0 && first[index-1].RouteRank >= option.RouteRank {
			t.Fatalf("Options() ranks are not strictly increasing: %+v", first)
		}
	}
	if !seenKinds["codex"] || !seenKinds["claude-code"] {
		t.Fatalf("Options() kinds = %+v; want Codex and Claude Code", first)
	}

	first[0] = Option{Kind: "mutated", RouteRank: -1}
	second := Options()
	if second[0].Kind == "mutated" || second[0].RouteRank < 0 {
		t.Fatalf("Options() leaked mutable package storage: %+v", second)
	}
}

func TestProviderCatalogCanonicalSelectedKindsValidatesAndReturnsFreshOrder(t *testing.T) {
	got, err := CanonicalSelectedKinds([]string{"claude-code", "codex"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"codex", "claude-code"}
	if !slices.Equal(got, want) {
		t.Fatalf("CanonicalSelectedKinds() = %q; want %q", got, want)
	}
	got[0] = "mutated"
	fresh, err := CanonicalSelectedKinds([]string{"claude-code", "codex"})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(fresh, want) {
		t.Fatalf("CanonicalSelectedKinds() leaked mutable result: %q", fresh)
	}

	empty, err := CanonicalSelectedKinds(nil)
	if err != nil {
		t.Fatal(err)
	}
	if empty == nil || len(empty) != 0 {
		t.Fatalf("CanonicalSelectedKinds(nil) = %#v; want fresh empty slice", empty)
	}
}

func TestProviderCatalogCanonicalSelectedKindsRejectsInvalidInput(t *testing.T) {
	tests := []struct {
		name  string
		input []string
		want  error
	}{
		{name: "duplicate", input: []string{"codex", "codex"}, want: ErrInvalidSelection},
		{name: "blank", input: []string{""}, want: ErrInvalidSelection},
		{name: "not exact", input: []string{" codex"}, want: ErrUnsupportedKind},
		{name: "unsupported", input: []string{"future-runtime"}, want: ErrUnsupportedKind},
		{
			name: "over limit",
			input: []string{
				"a", "b", "c", "d", "e", "f", "g", "h", "i",
			},
			want: ErrInvalidSelection,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := CanonicalSelectedKinds(test.input); !errors.Is(err, test.want) {
				t.Fatalf("CanonicalSelectedKinds(%q) error = %v; want %v", test.input, err, test.want)
			}
		})
	}
}
