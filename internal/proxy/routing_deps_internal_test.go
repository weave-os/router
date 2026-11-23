package proxy

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"

	"weave-os/router/internal/dispatch"
)

func TestWithDepsRoutingPrefixTrimFreeSwitch(t *testing.T) {
	disabled := false
	for name, tc := range map[string]struct {
		routing        *RoutingDeps
		wantPrefixTrim bool
		wantEffort     bool
	}{
		"nil routing keeps the defaults":       {routing: nil, wantPrefixTrim: true},
		"unset switch keeps prefix trim on":    {routing: &RoutingDeps{EffortEscalation: true}, wantPrefixTrim: true, wantEffort: true},
		"explicit false turns prefix trim off": {routing: &RoutingDeps{PrefixTrimFreeSwitch: &disabled}, wantPrefixTrim: false},
	} {
		t.Run(name, func(t *testing.T) {
			svc := WithDeps(NewService(nil, dispatch.NewClients(nil), nil, nil, false, "", "", nil), Deps{Routing: tc.routing})
			assert.Equal(t, tc.wantPrefixTrim, svc.routing.ResolvePrefixTrimFreeSwitch(context.Background()))
			assert.Equal(t, tc.wantEffort, svc.routing.ResolveEffortEscalation(context.Background()))
		})
	}
}
