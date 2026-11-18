package selectionpolicy

import (
	"fmt"

	"weave-os/router/internal/router/hmm/rosterdata"
)

type Policy struct {
	roster *rosterdata.Roster
}

func Admit(payload []byte) (*Policy, error) {
	roster, err := rosterdata.Parse(payload)
	if err != nil {
		return nil, err
	}
	roster.SHA256 = rosterdata.SHA256Hex(payload)
	if err := rosterdata.ValidateCatalog(roster); err != nil {
		return nil, err
	}
	if roster.SchemaVersion != rosterdata.SchemaVersionPolicyV1 {
		return nil, fmt.Errorf("selectionpolicy: admit requires schema %q, got %q", rosterdata.SchemaVersionPolicyV1, roster.SchemaVersion)
	}
	return &Policy{roster: roster}, nil
}

func (p *Policy) Roster() *rosterdata.Roster { return p.roster }

func (p *Policy) SHA256() string { return p.roster.SHA256 }
