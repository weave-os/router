package rosterdata

import (
	"fmt"
	"strings"
)

// Set is every declarative roster a deployment can serve, indexed by the file
// digest a policy pin names. Default is the roster used when no pin applies.
type Set struct {
	Default  *Roster
	BySHA256 map[string]*Roster
}

// NewSet indexes rosters by digest; the first is the default. Rosters without
// a digest are still servable as the default but cannot be pinned.
func NewSet(defaultRoster *Roster, additional ...*Roster) *Set {
	set := &Set{Default: defaultRoster, BySHA256: make(map[string]*Roster, 1+len(additional))}
	for _, roster := range append([]*Roster{defaultRoster}, additional...) {
		if roster != nil && roster.SHA256 != "" {
			set.BySHA256[roster.SHA256] = roster
		}
	}
	return set
}

// Lookup returns the roster for sha256, or the default when sha256 is empty.
func (s *Set) Lookup(sha256 string) (*Roster, bool) {
	if s == nil {
		return nil, false
	}
	if sha256 == "" {
		return s.Default, s.Default != nil
	}
	roster, ok := s.BySHA256[strings.ToLower(sha256)]
	return roster, ok
}

// SHA256s lists every pinnable roster digest.
func (s *Set) SHA256s() []string {
	if s == nil {
		return nil
	}
	digests := make([]string, 0, len(s.BySHA256))
	for digest := range s.BySHA256 {
		digests = append(digests, digest)
	}
	return digests
}

// LoadSet loads and fully validates the default roster plus every additional
// roster path at boot, so a pinned roster is never read on a live request.
func LoadSet(defaultPath string, additionalPaths []string) (*Set, error) {
	defaultRoster, err := Load(defaultPath)
	if err != nil {
		return nil, err
	}
	additional := make([]*Roster, 0, len(additionalPaths))
	for _, path := range additionalPaths {
		path = strings.TrimSpace(path)
		if path == "" {
			continue
		}
		roster, err := Load(path)
		if err != nil {
			return nil, fmt.Errorf("rosterdata: additional roster: %w", err)
		}
		additional = append(additional, roster)
	}
	return NewSet(defaultRoster, additional...), nil
}
