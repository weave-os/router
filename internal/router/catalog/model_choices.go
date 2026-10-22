package catalog

import (
	"fmt"

	"weave-os/router/internal/router"
)

// ScopeSelection includes automatic routers alongside explicit upstream models.
const ScopeSelection ModelListingScope = "selection"

// ModelListingScope selects a catalog projection for model discovery.
type ModelListingScope string

// ModelChoiceKind distinguishes automatic routers from upstream model pins.
type ModelChoiceKind string

const (
	ModelChoiceRouter ModelChoiceKind = "router"
	ModelChoiceDirect ModelChoiceKind = "direct"
)

// ModelChoice describes one ID accepted by the client model selector.
type ModelChoice struct {
	Model       string          `json:"model"`
	Description string          `json:"description"`
	Kind        ModelChoiceKind `json:"kind"`
}

// ModelChoicesResponse is the selection projection of the model catalog endpoint.
type ModelChoicesResponse struct {
	Models []ModelChoice `json:"models"`
}

// ModelChoices keeps routing aliases out of pricing, candidate pools and allowlists.
func ModelChoices() []ModelChoice {
	choices := []ModelChoice{
		{Model: string(router.ModelAutomatic), Description: "Automatically select an upstream model using the deployment's default router.", Kind: ModelChoiceRouter},
		{Model: string(router.ModelHMM), Description: "Automatically select an upstream model using the experimental HMM classifier. Managed use requires internal organization access.", Kind: ModelChoiceRouter},
		{Model: string(router.ModelQwen), Description: "Automatically select an upstream model with the Qwen classifier and its pinned roster. Requires internal organization access.", Kind: ModelChoiceRouter},
		{Model: string(router.ModelDecision), Description: "Automatically select an upstream model with the decision model task and call complexity judge. Requires internal organization access.", Kind: ModelChoiceRouter},
	}
	for _, entry := range Listing() {
		choices = append(choices, ModelChoice{
			Model: entry.Model, Kind: ModelChoiceDirect,
			Description: fmt.Sprintf("Pin this upstream model; primary provider: %s.", entry.Provider),
		})
	}
	return choices
}
