package cache

import (
	"crypto/sha256"
	"encoding/json"

	"weave-os/router/internal/router/catalog"
)

type Product string

const (
	ProductLegacy Product = "legacy"
	ProductBoost  Product = "boost"
	ProductMax    Product = "max"
)

// ProvenanceScope contains verified identity and dispatch facts, never raw
// credentials. Optional serving identifiers distinguish admitted revisions.
type ProvenanceScope struct {
	CredentialSubject string
	Product           Product
	Profile           string
	ProfileRevision   string
	Release           string
	Binding           string
	Model             catalog.ModelID
	Provider          string
	UpstreamScope     string
}

// Provenance is an immutable replay scope. Its digest keeps subject/profile
// identifiers out of bucket keys and diagnostics.
type Provenance struct {
	digest   [32]byte
	model    string
	provider string
}

// NewProvenance fails closed when required isolation dimensions are absent.
func NewProvenance(scope ProvenanceScope) Provenance {
	if (scope.Profile == "") != (scope.ProfileRevision == "") || (scope.Release == "") != (scope.Binding == "") {
		return Provenance{}
	}
	if scope.CredentialSubject == "" || scope.Product == "" || scope.Model == "" || scope.Provider == "" || scope.UpstreamScope == "" {
		return Provenance{}
	}
	switch scope.Product {
	case ProductLegacy, ProductBoost, ProductMax:
	default:
		return Provenance{}
	}
	encoded, _ := json.Marshal(scope)
	return Provenance{digest: sha256.Sum256(encoded), model: scope.Model.String(), provider: scope.Provider}
}

// Valid reports whether provenance has a complete verified scope.
func (p Provenance) Valid() bool { return p.digest != [32]byte{} }

// Model is the target that produced the replayed body.
func (p Provenance) Model() string { return p.model }

// Provider is the binding that produced the replayed body.
func (p Provenance) Provider() string { return p.provider }
