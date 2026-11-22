package routingpolicy_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"weave-os/router/internal/router"
	"weave-os/router/internal/router/turntype"
	"weave-os/router/internal/routingpolicy"
)

func TestAdmitClassifierTurn(t *testing.T) {
	tests := map[string]struct {
		turnType turntype.TurnType
		captured bool
		request  router.Request
		want     error
	}{
		"main loop with captured input":   {turnType: turntype.MainLoop, captured: true},
		"tool result with captured input": {turnType: turntype.ToolResult, captured: true},
		"title generation is a utility":   {turnType: turntype.TitleGen, captured: true, want: router.ErrClassifierHistoryUnavailable},
		"compaction is a utility":         {turnType: turntype.Compaction, captured: true, want: router.ErrClassifierHistoryUnavailable},
		"utility outranks missing input":  {turnType: turntype.Probe, want: router.ErrClassifierHistoryUnavailable},
		"missing input":                   {turnType: turntype.MainLoop, want: router.ErrClassifierThreadInvalid},
		"forced model":                    {turnType: turntype.MainLoop, captured: true, request: router.Request{ForceModel: "claude-sonnet-4-5"}, want: router.ErrClassifierThreadInvalid},
		"forced cluster":                  {turnType: turntype.Recap, captured: true, request: router.Request{ForceCluster: "7"}, want: router.ErrClassifierThreadInvalid},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			assert.ErrorIs(t, routingpolicy.AdmitClassifierTurn(tt.turnType, tt.captured, tt.request), tt.want)
		})
	}
}
