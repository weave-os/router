package rosterdata

import (
	"fmt"
	"strings"
)

type Set struct {
	Default  *Roster
	BySHA256 map[string]*Roster
}

func NewSet(defaultRoster *Roster, additional ...*Roster) *Set {
	set := &Set{Default: defaultRoster, BySHA256: make(map[string]*Roster, 1+len(additional))}
	for _, roster := range append([]*Roster{defaultRoster}, additional...) {
		if roster != nil && roster.SHA256 != "" {
			set.BySHA256[roster.SHA256] = roster
		}
	}
	return set
}

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
