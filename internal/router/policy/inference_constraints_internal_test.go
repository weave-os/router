package policy

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
)

func TestContextWindowConstraintDefersToOverflowAdmission(t *testing.T) {
	binding := plannedBinding{Binding: Binding{CatalogID: "claude-opus-4-8"}}
	request := router.Request{EstimatedInputTokens: catalog.ContextWindowFor("claude-opus-4-8") + 1}

	assert.NotEmpty(t, constraintViolation(ConstraintContextWindow, request, BudgetSpec{}, binding))

	request.OverflowAdmittedModels = map[string]struct{}{"claude-opus-4-8": {}}
	assert.Empty(t, constraintViolation(ConstraintContextWindow, request, BudgetSpec{}, binding),
		"the plan's proof must agree with the resolver that kept the admitted model")
}
