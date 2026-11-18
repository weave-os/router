package selectionpolicy

import (
	"bytes"
	"fmt"

	"weave-os/router/internal/router/hmm/rosterdata"
)

type Policy struct {
	admittedPayload []byte
	sha256          string
}

func Admit(payload []byte) (*Policy, error) {
	roster, err := rosterdata.Parse(payload)
	if err != nil {
		return nil, err
	}
	if err := rosterdata.ValidateCatalog(roster); err != nil {
		return nil, err
	}
	if roster.SchemaVersion != rosterdata.SchemaVersionPolicyV1 {
		return nil, fmt.Errorf("selectionpolicy: admit requires schema %q, got %q", rosterdata.SchemaVersionPolicyV1, roster.SchemaVersion)
	}
	return &Policy{admittedPayload: bytes.Clone(payload), sha256: rosterdata.SHA256Hex(payload)}, nil
}

func (p *Policy) Roster() *rosterdata.Roster {
	roster, err := rosterdata.Parse(p.admittedPayload)
	if err != nil {
		panic("selectionpolicy: admitted payload no longer decodes: " + err.Error())
	}
	roster.SHA256 = p.sha256
	return roster
}

func (p *Policy) SHA256() string { return p.sha256 }
