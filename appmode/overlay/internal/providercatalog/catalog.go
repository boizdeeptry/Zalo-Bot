// Package providercatalog owns the safe, data-only Provider descriptors shared
// by Store and daemon onboarding code. It deliberately contains no command,
// credential, filesystem or runtime-driver details.
package providercatalog

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	MaxSelectedKinds = 8

	maxKindBytes        = 64
	maxDisplayNameBytes = 128
	maxDescriptionBytes = 1024
	maxRouteRank        = int64(1<<31 - 1)
)

var (
	ErrInvalidCatalog   = errors.New("invalid provider catalog")
	ErrInvalidSelection = errors.New("invalid provider selection")
	ErrUnsupportedKind  = errors.New("unsupported provider kind")
)

type Option struct {
	Kind        string
	DisplayName string
	Description string
	Recommended bool
	Beta        bool
	Advertised  bool
	RouteRank   int
}

// Catalog is an immutable, route-ranked collection of Provider options. Its
// zero value is intentionally invalid so a missing catalog cannot authorize a
// Provider selection.
type Catalog struct {
	valid   bool
	options []Option
	byKind  map[string]Option
}

// New validates and defensively copies a Provider catalog. The returned value
// has no mutation API and keeps all reference-backed storage private.
func New(options []Option) (Catalog, error) {
	if len(options) > MaxSelectedKinds {
		return Catalog{}, fmt.Errorf(
			"%w: contains %d Providers, maximum is %d",
			ErrInvalidCatalog,
			len(options),
			MaxSelectedKinds,
		)
	}

	owned := make([]Option, len(options))
	copy(owned, options)
	seenKinds := make(map[string]struct{}, len(owned))
	seenRanks := make(map[int]string, len(owned))
	for _, option := range owned {
		if !ValidKind(option.Kind) {
			return Catalog{}, fmt.Errorf("%w: invalid Provider kind %q", ErrInvalidCatalog, option.Kind)
		}
		if _, exists := seenKinds[option.Kind]; exists {
			return Catalog{}, fmt.Errorf("%w: duplicate Provider kind %q", ErrInvalidCatalog, option.Kind)
		}
		seenKinds[option.Kind] = struct{}{}

		if !validMetadata(option.DisplayName, maxDisplayNameBytes) {
			return Catalog{}, fmt.Errorf("%w: invalid display name for %q", ErrInvalidCatalog, option.Kind)
		}
		if !validMetadata(option.Description, maxDescriptionBytes) {
			return Catalog{}, fmt.Errorf("%w: invalid description for %q", ErrInvalidCatalog, option.Kind)
		}
		if option.RouteRank < 0 || int64(option.RouteRank) > maxRouteRank {
			return Catalog{}, fmt.Errorf("%w: invalid route rank for %q", ErrInvalidCatalog, option.Kind)
		}
		if previous, exists := seenRanks[option.RouteRank]; exists {
			return Catalog{}, fmt.Errorf(
				"%w: route rank %d is shared by %q and %q",
				ErrInvalidCatalog,
				option.RouteRank,
				previous,
				option.Kind,
			)
		}
		seenRanks[option.RouteRank] = option.Kind
	}

	sort.Slice(owned, func(i, j int) bool {
		return owned[i].RouteRank < owned[j].RouteRank
	})
	byKind := make(map[string]Option, len(owned))
	for _, option := range owned {
		byKind[option.Kind] = option
	}
	return Catalog{valid: true, options: owned, byKind: byKind}, nil
}

// Default returns the production Catalog as an independent immutable value.
func Default() Catalog {
	catalog, err := New([]Option{
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
	})
	if err != nil {
		return Catalog{}
	}
	return catalog
}

// Options returns a defensive copy in route-rank order. An invalid Catalog has
// no visible options.
func (c Catalog) Options() []Option {
	if !c.valid {
		return make([]Option, 0)
	}
	result := make([]Option, len(c.options))
	copy(result, c.options)
	return result
}

// Option returns safe metadata for an exact kind string. Unadvertised options
// are included so validated registry code can inspect their capability.
func (c Catalog) Option(kind string) (Option, bool) {
	if !c.valid {
		return Option{}, false
	}
	option, exists := c.byKind[kind]
	return option, exists
}

// CanonicalSelectedKinds validates a bounded set of exact advertised kinds and
// returns a fresh slice sorted by catalog route rank. Request order never
// affects fallback priority.
func (c Catalog) CanonicalSelectedKinds(input []string) ([]string, error) {
	if !c.valid {
		return nil, fmt.Errorf("%w: Catalog is not initialized", ErrInvalidCatalog)
	}
	if len(input) > MaxSelectedKinds {
		return nil, fmt.Errorf(
			"%w: selected %d Providers, maximum is %d",
			ErrInvalidSelection,
			len(input),
			MaxSelectedKinds,
		)
	}
	selected := make([]Option, 0, len(input))
	seen := make(map[string]struct{}, len(input))
	for _, kind := range input {
		if kind == "" {
			return nil, fmt.Errorf("%w: Provider kind is blank", ErrInvalidSelection)
		}
		if _, exists := seen[kind]; exists {
			return nil, fmt.Errorf("%w: duplicate Provider kind %q", ErrInvalidSelection, kind)
		}
		seen[kind] = struct{}{}
		option, exists := c.Option(kind)
		if !exists || !option.Advertised {
			return nil, fmt.Errorf("%w: %q", ErrUnsupportedKind, kind)
		}
		selected = append(selected, option)
	}
	sort.Slice(selected, func(i, j int) bool {
		return selected[i].RouteRank < selected[j].RouteRank
	})
	result := make([]string, len(selected))
	for index, option := range selected {
		result[index] = option.Kind
	}
	return result, nil
}

// Options returns the default production options for compatibility with
// existing callers.
func Options() []Option {
	return Default().Options()
}

// OptionForKind inspects the default production Catalog for compatibility with
// existing callers.
func OptionForKind(kind string) (Option, bool) {
	return Default().Option(kind)
}

// CanonicalSelectedKinds canonicalizes through the default production Catalog
// for compatibility with existing callers.
func CanonicalSelectedKinds(input []string) ([]string, error) {
	return Default().CanonicalSelectedKinds(input)
}

// ValidKind reports whether kind uses the bounded canonical Provider-kind
// grammar accepted by Catalog.
func ValidKind(kind string) bool {
	if len(kind) == 0 || len(kind) > maxKindBytes {
		return false
	}
	for index := 0; index < len(kind); index++ {
		value := kind[index]
		switch {
		case value >= 'a' && value <= 'z':
		case index > 0 && value >= '0' && value <= '9':
		case index > 0 && index < len(kind)-1 && value == '-' && kind[index-1] != '-':
		default:
			return false
		}
	}
	return true
}

func validMetadata(value string, maxBytes int) bool {
	if value == "" || len(value) > maxBytes || !utf8.ValidString(value) || strings.TrimSpace(value) != value {
		return false
	}
	for _, runeValue := range value {
		if unicode.IsControl(runeValue) || unicode.In(runeValue, unicode.Cf, unicode.Zl, unicode.Zp) {
			return false
		}
	}
	return true
}
