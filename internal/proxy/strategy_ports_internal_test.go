package proxy

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"weave-os/router/internal/router/continuity"
	"weave-os/router/internal/router/hmm"
)

func TestWithStrategyPortsFailsFastOnUnwiredPorts(t *testing.T) {
	assert.PanicsWithError(t, "strategy ports not wired: Ownership, Facts, Upgrade, CostGate", func() {
		WithStrategyPorts(NewService(nil, nil, nil, nil, false, "", "", nil), continuity.StrategyPorts{})
	})
}

func TestWithStrategyPortsWiresServingPorts(t *testing.T) {
	ports := hmm.ServingPorts()
	svc := WithStrategyPorts(NewService(nil, nil, nil, nil, false, "", "", nil), ports)
	assert.Equal(t, ports, svc.strategyPorts)
}
