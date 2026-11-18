package selectionpolicy

import (
	"bytes"
	"encoding/json"
	"fmt"

	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/hmm/armid"
	"weave-os/router/internal/router/hmm/rosterdata"
)

type Policy struct {
	admittedPayload []byte
	admittedRoster  *rosterdata.Roster
	catalogIDs      map[string]string
}

func Admit(payload []byte) (*Policy, error) {
	roster, err := rosterdata.Parse(payload)
	if err != nil {
		return nil, err
	}
	roster.SHA256 = rosterdata.SHA256Hex(payload)
	return admit(payload, roster)
}

func AdmitDecoded(roster *rosterdata.Roster) (*Policy, error) {
	payload, err := json.Marshal(roster)
	if err != nil {
		return nil, fmt.Errorf("selectionpolicy: encode decoded roster: %w", err)
	}
	admittedCopy, err := rosterdata.Parse(payload)
	if err != nil {
		return nil, err
	}
	admittedCopy.SHA256 = roster.SHA256
	return admit(payload, admittedCopy)
}

func admit(payload []byte, roster *rosterdata.Roster) (*Policy, error) {
	if err := rosterdata.ValidateCatalog(roster); err != nil {
		return nil, err
	}
	if roster.SchemaVersion != rosterdata.SchemaVersionPolicyV1 {
		return nil, fmt.Errorf("selectionpolicy: admit requires schema %q, got %q", rosterdata.SchemaVersionPolicyV1, roster.SchemaVersion)
	}
	return &Policy{admittedPayload: bytes.Clone(payload), admittedRoster: roster, catalogIDs: catalogIDsByArm(roster)}, nil
}

func (p *Policy) Roster() *rosterdata.Roster {
	roster, _ := rosterdata.Parse(p.admittedPayload)
	roster.SHA256 = p.admittedRoster.SHA256
	return roster
}

func (p *Policy) SHA256() string { return p.admittedRoster.SHA256 }

func (p *Policy) CatalogID(arm string) string {
	if catalogID, indexed := p.catalogIDs[arm]; indexed {
		return catalogID
	}
	return armid.CatalogIDForRoster(arm)
}

func (p *Policy) DeployedModels() []router.DeployedEntry {
	arms := p.admittedRoster.AllArms()
	deployed := make([]router.DeployedEntry, 0, len(arms))
	listed := make(map[string]struct{}, len(arms))
	for _, arm := range arms {
		model, _ := catalog.ByID(p.CatalogID(arm))
		if _, duplicate := listed[model.ID]; duplicate {
			continue
		}
		listed[model.ID] = struct{}{}
		deployed = append(deployed, router.DeployedEntry{Model: model.ID, Provider: model.PrimaryProvider()})
	}
	return deployed
}

func (p *Policy) ClusterArms() map[string][]string {
	clusters := make(map[string][]string, len(p.admittedRoster.Clusters))
	for label, cluster := range p.admittedRoster.Clusters {
		clusters[label] = append([]string(nil), cluster.Arms...)
	}
	return clusters
}

func (p *Policy) HarnessClusterArms() map[string]map[string][]string {
	harnesses := make(map[string]map[string][]string)
	for _, cluster := range p.admittedRoster.Clusters {
		for harness := range cluster.ArmsByHarness {
			harnesses[string(harness)] = p.clusterArmsFor(harness)
		}
	}
	return harnesses
}

func (p *Policy) clusterArmsFor(harness rosterdata.Harness) map[string][]string {
	clusters := make(map[string][]string, len(p.admittedRoster.Clusters))
	for label, cluster := range p.admittedRoster.Clusters {
		arms := cluster.ArmsByHarness[harness]
		if len(arms) == 0 {
			arms = cluster.Arms
		}
		clusters[label] = append([]string(nil), arms...)
	}
	return clusters
}

func catalogIDsByArm(roster *rosterdata.Roster) map[string]string {
	arms := roster.AllArms()
	for _, cluster := range roster.Clusters {
		for arm := range cluster.ArmScores {
			arms = append(arms, arm)
		}
		for arm := range cluster.ArmIndices {
			arms = append(arms, arm)
		}
		for _, pins := range cluster.ManualPinsByHarness {
			arms = append(arms, pins...)
		}
	}
	catalogIDs := make(map[string]string, len(arms))
	for _, arm := range arms {
		if _, indexed := catalogIDs[arm]; !indexed {
			catalogIDs[arm] = armid.CatalogIDForRoster(arm)
		}
	}
	return catalogIDs
}
