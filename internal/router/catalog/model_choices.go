package catalog

import (
	"fmt"

	"weave-os/router/internal/router"
)

const ScopeSelection ModelListingScope = "selection"

type ModelListingScope string

type ModelChoiceKind string

const (
	ModelChoiceRouter ModelChoiceKind = "router"
	ModelChoiceDirect ModelChoiceKind = "direct"
)

type ModelChoice struct {
	Model       string          `json:"model"`
	Description string          `json:"description"`
	Kind        ModelChoiceKind `json:"kind"`
}

type ModelChoicesResponse struct {
	Models []ModelChoice `json:"models"`
}

func ModelChoices() []ModelChoice {
	choices := []ModelChoice{
		{Model: string(router.ModelAutomatic), Description: "Automatically select an upstream model using the deployment's default router.", Kind: ModelChoiceRouter},
		{Model: string(router.ModelHMM), Description: "Automatically select an upstream model using the primary HMM classifier. Managed use requires internal organization access.", Kind: ModelChoiceRouter},
		{Model: string(router.ModelHMMBeta), Description: "Automatically select an upstream model with the beta HMM classifier and its pinned roster, or the primary HMM classifier when no beta is released. Requires internal organization access.", Kind: ModelChoiceRouter},
		{Model: string(router.ModelQwen), Description: "Automatically select an upstream model with the Qwen classifier and its pinned roster, or the primary HMM classifier when Qwen is not released. Requires internal organization access.", Kind: ModelChoiceRouter},
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
