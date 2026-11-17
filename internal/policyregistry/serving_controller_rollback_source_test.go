package policyregistry

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestRollbackSourceRequiredForRollbackScopeAndAnyWithdrawal(t *testing.T) {
	assert.False(t, requiresRollbackSource(ProposalView{Scope: ChangeFull}))
	assert.True(t, requiresRollbackSource(ProposalView{Scope: ChangeRollback}))
	assert.True(t, requiresRollbackSource(ProposalView{Scope: ChangeFull, WithdrawActivations: []string{"activation-1"}}), "an emergency withdrawal must prove its source release")
}
