// Package providercatalog owns the safe, data-only Provider descriptors shared
// by Store and daemon onboarding code. It deliberately contains no command,
// credential, filesystem or runtime-driver details.
package providercatalog

import (
	"errors"
	"fmt"
	"sort"
)

const MaxSelectedKinds = 8

var (
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

var options = [...]Option{
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

// Options returns a defensive copy in route-rank order.
func Options() []Option {
	result := make([]Option, len(options))
	copy(result, options[:])
	sort.Slice(result, func(i, j int) bool {
		return result[i].RouteRank < result[j].RouteRank
	})
	return result
}

// OptionForKind returns safe metadata for an exact kind string. Hidden options
// are included so registry code can inspect their Advertised capability.
func OptionForKind(kind string) (Option, bool) {
	for _, option := range options {
		if option.Kind == kind {
			return option, true
		}
	}
	return Option{}, false
}

// CanonicalSelectedKinds validates a bounded set of exact advertised kinds and
// returns a fresh slice sorted by catalog route rank. Request order never affects
// fallback priority.
func CanonicalSelectedKinds(input []string) ([]string, error) {
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
		option, exists := OptionForKind(kind)
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
