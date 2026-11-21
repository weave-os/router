package floor_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"

	"weave-os/router/internal/router"
	"weave-os/router/internal/router/escalation/floor"
)

func TestSettleLLMKeepsTheServedDecisionWithoutAnLLMTurn(t *testing.T) {
	helper := floor.New(nil, nil)
	decision := router.Decision{Model: "claude-sonnet-4-5", Reason: "escalation_promoted"}

	assert.False(t, helper.SettleLLM(context.Background(), nil, decision, nil))
	assert.False(t, helper.SettleLLM(context.Background(), nil, decision, errors.New("route failed")))
}
