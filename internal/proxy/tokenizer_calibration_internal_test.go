package proxy

import (
	"context"
	"strings"
	"testing"

	"weave-os/router/internal/dispatch"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/sessionpin"
	"weave-os/router/internal/router/turntype"

	"github.com/stretchr/testify/assert"
)

func TestStripRequestedProviderQualifier(t *testing.T) {
	slashed := 0
	for _, m := range catalog.Models {
		if !strings.Contains(m.ID, "/") {
			continue
		}
		slashed++
		assert.Equal(t, m.ID, stripRequestedProviderQualifier(m.ID), "a catalog id that contains a slash is kept whole")
	}
	assert.Positive(t, slashed, "catalog has no slash-containing id left to pin")
	assert.Equal(t, "claude-opus-5-5", stripRequestedProviderQualifier("anthropic/claude-opus-5-5"))
	assert.Equal(t, "nobody/unknown-model", stripRequestedProviderQualifier("nobody/unknown-model"))
}

type calibrationRecordingPinStore struct {
	overwritingPinStore
	writes int
}

func (s *calibrationRecordingPinStore) UpdateTokenizerCalibration(context.Context, [sessionpin.SessionKeyLen]byte, string, router.Strategy, sessionpin.TokenizerCalibration) error {
	s.writes++
	return nil
}

func TestRecordTokenizerObservation_OnlyConversationTurnsCalibrate(t *testing.T) {
	for _, tt := range []struct {
		turnType turntype.TurnType
		want     int
	}{
		{turntype.MainLoop, 1},
		{turntype.ToolResult, 1},
		{turntype.SubAgentDispatch, 0},
		{turntype.TitleGen, 0},
		{turntype.Compaction, 0},
	} {
		t.Run(string(tt.turnType), func(t *testing.T) {
			store := &calibrationRecordingPinStore{}
			svc := &Service{engine: dispatch.NewEngine(nil), strategyServing: hmmPorts(), pinStore: store}
			svc.recordTokenizerObservation(context.Background(), turnLoopResult{TurnType: tt.turnType}, "default", providers.ProviderOpenAI, 500_000, 0, 0)
			assert.Equal(t, tt.want, store.writes)
		})
	}
}
