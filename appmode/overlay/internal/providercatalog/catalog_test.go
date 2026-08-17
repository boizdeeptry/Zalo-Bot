package providercatalog

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
)

func TestValidKind(t *testing.T) {
	valid := []string{
		"a",
		"codex",
		"claude-code",
		"future-cli",
		"provider-2",
		strings.Repeat("a", 64),
	}
	for _, kind := range valid {
		t.Run("valid "+kind, func(t *testing.T) {
			if !ValidKind(kind) {
				t.Fatalf("ValidKind(%q) = false; want true", kind)
			}
		})
	}

	invalid := []struct {
		name string
		kind string
	}{
		{name: "empty", kind: ""},
		{name: "over 64 bytes", kind: strings.Repeat("a", 65)},
		{name: "uppercase", kind: "Codex"},
		{name: "leading hyphen", kind: "-codex"},
		{name: "trailing hyphen", kind: "codex-"},
		{name: "double hyphen", kind: "co--dex"},
		{name: "slash path", kind: "codex/provider"},
		{name: "relative path", kind: "../codex"},
		{name: "control", kind: "codex\x00"},
		{name: "non ASCII", kind: "c\u00f3dex"},
		{name: "Unicode confusable", kind: "c\u043edex"},
	}
	for _, test := range invalid {
		t.Run(test.name, func(t *testing.T) {
			if ValidKind(test.kind) {
				t.Fatalf("ValidKind(%q) = true; want false", test.kind)
			}
		})
	}
}

func TestCatalogCanonicalizesSyntheticProviderByRank(t *testing.T) {
	catalog, err := New(testCatalogOptions())
	if err != nil {
		t.Fatal(err)
	}

	gotOptions := catalog.Options()
	wantOptions := []Option{
		testCatalogOption("codex", "Codex", 10),
		testCatalogOption("future-cli", "Future CLI", 50),
		testCatalogOption("claude-code", "Claude Code", 100),
	}
	if !slices.Equal(gotOptions, wantOptions) {
		t.Fatalf("Catalog.Options() = %+v; want %+v", gotOptions, wantOptions)
	}

	gotKinds, err := catalog.CanonicalSelectedKinds([]string{"claude-code", "future-cli", "codex"})
	if err != nil {
		t.Fatal(err)
	}
	wantKinds := []string{"codex", "future-cli", "claude-code"}
	if !slices.Equal(gotKinds, wantKinds) {
		t.Fatalf("Catalog.CanonicalSelectedKinds() = %q; want %q", gotKinds, wantKinds)
	}

	gotFuture, ok := catalog.Option("future-cli")
	if !ok || gotFuture != wantOptions[1] {
		t.Fatalf("Catalog.Option(future-cli) = (%+v, %t); want (%+v, true)", gotFuture, ok, wantOptions[1])
	}
}

func TestCatalogDefensivelyCopiesInputAndOutput(t *testing.T) {
	input := testCatalogOptions()
	catalog, err := New(input)
	if err != nil {
		t.Fatal(err)
	}

	input[0] = testCatalogOption("mutated-input", "Mutated Input", 1)
	if option, ok := catalog.Option("claude-code"); !ok || option.RouteRank != 100 {
		t.Fatalf("Catalog retained mutable constructor input: (%+v, %t)", option, ok)
	}
	if _, ok := catalog.Option("mutated-input"); ok {
		t.Fatal("Catalog exposed mutation of constructor input")
	}

	firstOptions := catalog.Options()
	firstOptions[0] = testCatalogOption("mutated-output", "Mutated Output", 2)
	secondOptions := catalog.Options()
	if secondOptions[0].Kind != "codex" || secondOptions[0].RouteRank != 10 {
		t.Fatalf("Catalog.Options() leaked mutable storage: %+v", secondOptions)
	}

	firstKinds, err := catalog.CanonicalSelectedKinds([]string{"claude-code", "codex"})
	if err != nil {
		t.Fatal(err)
	}
	firstKinds[0] = "mutated-output"
	secondKinds, err := catalog.CanonicalSelectedKinds([]string{"claude-code", "codex"})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(secondKinds, []string{"codex", "claude-code"}) {
		t.Fatalf("Catalog.CanonicalSelectedKinds() leaked mutable result: %q", secondKinds)
	}
}

func TestCatalogCanonicalSelectedKindsRejectsInvalidInput(t *testing.T) {
	catalog := Default()
	callers := []struct {
		name         string
		canonicalize func([]string) ([]string, error)
	}{
		{name: "Catalog method", canonicalize: catalog.CanonicalSelectedKinds},
		{name: "compatibility wrapper", canonicalize: CanonicalSelectedKinds},
	}
	tests := []struct {
		name  string
		input []string
		want  error
	}{
		{name: "duplicate", input: []string{"codex", "codex"}, want: ErrInvalidSelection},
		{name: "blank", input: []string{""}, want: ErrInvalidSelection},
		{name: "non-exact", input: []string{" codex"}, want: ErrUnsupportedKind},
		{name: "unsupported", input: []string{"future-cli"}, want: ErrUnsupportedKind},
		{
			name:  "over limit",
			input: []string{"a", "b", "c", "d", "e", "f", "g", "h", "i"},
			want:  ErrInvalidSelection,
		},
	}

	for _, caller := range callers {
		t.Run(caller.name, func(t *testing.T) {
			for _, test := range tests {
				t.Run(test.name, func(t *testing.T) {
					if _, err := caller.canonicalize(test.input); !errors.Is(err, test.want) {
						t.Fatalf("CanonicalSelectedKinds(%q) error = %v; want %v", test.input, err, test.want)
					}
				})
			}
		})
	}
}

func TestCatalogRejectsInvalidOptions(t *testing.T) {
	maxInt := int(^uint(0) >> 1)
	tests := []struct {
		name    string
		options []Option
	}{
		{name: "blank kind", options: []Option{testCatalogOption("", "Future CLI", 50)}},
		{name: "uppercase kind", options: []Option{testCatalogOption("Future-CLI", "Future CLI", 50)}},
		{name: "path separator kind", options: []Option{testCatalogOption("future/cli", "Future CLI", 50)}},
		{name: "backslash kind", options: []Option{testCatalogOption(`future\cli`, "Future CLI", 50)}},
		{name: "unicode confusable kind", options: []Option{testCatalogOption("future-clі", "Future CLI", 50)}},
		{name: "oversize kind", options: []Option{testCatalogOption(strings.Repeat("a", 1024), "Future CLI", 50)}},
		{
			name: "duplicate kind",
			options: []Option{
				testCatalogOption("future-cli", "Future CLI", 50),
				testCatalogOption("future-cli", "Other Future CLI", 60),
			},
		},
		{
			name: "duplicate rank",
			options: []Option{
				testCatalogOption("future-cli", "Future CLI", 50),
				testCatalogOption("other-cli", "Other CLI", 50),
			},
		},
		{name: "blank display", options: []Option{testCatalogOption("future-cli", " ", 50)}},
		{name: "oversize display", options: []Option{testCatalogOption("future-cli", strings.Repeat("D", 4096), 50)}},
		{
			name: "blank description",
			options: []Option{{
				Kind:        "future-cli",
				DisplayName: "Future CLI",
				Description: "\t",
				Advertised:  true,
				RouteRank:   50,
			}},
		},
		{
			name: "oversize description",
			options: []Option{{
				Kind:        "future-cli",
				DisplayName: "Future CLI",
				Description: strings.Repeat("D", 16384),
				Advertised:  true,
				RouteRank:   50,
			}},
		},
		{name: "negative rank", options: []Option{testCatalogOption("future-cli", "Future CLI", -1)}},
		{name: "overflow rank", options: []Option{testCatalogOption("future-cli", "Future CLI", maxInt)}},
		{name: "too many options", options: testCatalogOptionsOverLimit()},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := New(test.options); err == nil {
				t.Fatalf("New(%+v) error = nil; want invalid catalog error", test.options)
			}
		})
	}

	t.Run("unadvertised option cannot be selected", func(t *testing.T) {
		option := testCatalogOption("future-cli", "Future CLI", 50)
		option.Advertised = false
		catalog, err := New([]Option{option})
		if err != nil {
			t.Fatalf("New() rejected inspectable unadvertised option: %v", err)
		}
		if _, err := catalog.CanonicalSelectedKinds([]string{"future-cli"}); !errors.Is(err, ErrUnsupportedKind) {
			t.Fatalf("Catalog.CanonicalSelectedKinds() error = %v; want %v", err, ErrUnsupportedKind)
		}
	})
}

func TestZeroCatalogFailsClosed(t *testing.T) {
	var catalog Catalog

	if got := catalog.Options(); len(got) != 0 {
		t.Fatalf("zero Catalog.Options() = %+v; want no options", got)
	}
	if option, ok := catalog.Option("codex"); ok || option != (Option{}) {
		t.Fatalf("zero Catalog.Option(codex) = (%+v, %t); want zero, false", option, ok)
	}
	for _, input := range [][]string{nil, {}, {"codex"}} {
		if got, err := catalog.CanonicalSelectedKinds(input); err == nil {
			t.Fatalf("zero Catalog.CanonicalSelectedKinds(%q) = (%q, nil); want fail closed", input, got)
		}
	}
}

func TestDefaultCatalogCompatibility(t *testing.T) {
	catalog := Default()
	wantOptions := []Option{
		{
			Kind:        "codex",
			DisplayName: "Codex",
			Description: "Kết nối tài khoản ChatGPT/Codex trên máy này",
			Recommended: true,
			Advertised:  true,
			RouteRank:   10,
		},
		{
			Kind:        "claude-code",
			DisplayName: "Claude Code",
			Description: "Kết nối tài khoản Claude Code trên máy này",
			Advertised:  true,
			RouteRank:   100,
		},
	}

	if got := catalog.Options(); !slices.Equal(got, wantOptions) {
		t.Fatalf("Default().Options() = %+v; want %+v", got, wantOptions)
	}
	if got := Options(); !slices.Equal(got, wantOptions) {
		t.Fatalf("Options() = %+v; want %+v", got, wantOptions)
	}
	for _, kind := range []string{"codex", "claude-code", "future-cli"} {
		methodOption, methodOK := catalog.Option(kind)
		compatOption, compatOK := OptionForKind(kind)
		if methodOption != compatOption || methodOK != compatOK {
			t.Fatalf("Option compatibility for %q: method=(%+v, %t), package=(%+v, %t)", kind, methodOption, methodOK, compatOption, compatOK)
		}
	}

	input := []string{"claude-code", "codex"}
	methodKinds, methodErr := catalog.CanonicalSelectedKinds(input)
	compatKinds, compatErr := CanonicalSelectedKinds(input)
	if methodErr != nil || compatErr != nil {
		t.Fatalf("canonical compatibility errors: method=%v, package=%v", methodErr, compatErr)
	}
	if !slices.Equal(methodKinds, compatKinds) || !slices.Equal(methodKinds, []string{"codex", "claude-code"}) {
		t.Fatalf("canonical compatibility: method=%q, package=%q", methodKinds, compatKinds)
	}

	if _, err := catalog.CanonicalSelectedKinds([]string{"future-cli"}); !errors.Is(err, ErrUnsupportedKind) {
		t.Fatalf("Default catalog accepted synthetic kind: %v", err)
	}
}

func testCatalogOptions() []Option {
	return []Option{
		testCatalogOption("claude-code", "Claude Code", 100),
		testCatalogOption("future-cli", "Future CLI", 50),
		testCatalogOption("codex", "Codex", 10),
	}
}

func testCatalogOption(kind, displayName string, routeRank int) Option {
	return Option{
		Kind:        kind,
		DisplayName: displayName,
		Description: "Connect " + displayName,
		Advertised:  true,
		RouteRank:   routeRank,
	}
}

func testCatalogOptionsOverLimit() []Option {
	options := make([]Option, MaxSelectedKinds+1)
	for index := range options {
		options[index] = testCatalogOption(
			fmt.Sprintf("provider-%d", index),
			fmt.Sprintf("Provider %d", index),
			index,
		)
	}
	return options
}
