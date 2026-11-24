package proxy

import (
	"context"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"weave-os/router/internal/credentials/usage"
	"weave-os/router/internal/dispatch"
	"weave-os/router/internal/flags"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/providers/anthropic"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/subscriptions"
)

func TestOwnSeatFirstOrdersCallerLoginAgainstManagedPool(t *testing.T) {
	const poolSeat = "synthetic-pool-seat"
	for _, scenario := range []struct {
		name                 string
		personalPlan         bool
		ownSeatFirst         bool
		ownOverageBeforePool bool
		observed             *usage.Snapshot
		wantAuthorization    string
		wantPoolLeases       int
	}{
		{name: "off leases the pool", wantAuthorization: poolSeat, wantPoolLeases: 1},
		{name: "on serves the caller login", ownSeatFirst: true, wantAuthorization: parityAnthropicToken},
		{name: "caller login in overage moves to the pool", ownSeatFirst: true, observed: &usage.Snapshot{OverageInUse: true}, wantAuthorization: poolSeat, wantPoolLeases: 1},
		{name: "own overage before pool keeps the caller login", ownSeatFirst: true, ownOverageBeforePool: true, observed: &usage.Snapshot{OverageInUse: true, Primary: usage.Window{UsedPercent: 1, WindowMinutes: 300}}, wantAuthorization: parityAnthropicToken},
		{name: "personal plan overage before pool keeps the caller login", personalPlan: true, ownSeatFirst: true, ownOverageBeforePool: true, observed: &usage.Snapshot{OverageInUse: true, Primary: usage.Window{UsedPercent: 1, WindowMinutes: 300}}, wantAuthorization: parityAnthropicToken},
		{name: "personal plan overage moves to the pool", personalPlan: true, ownSeatFirst: true, observed: &usage.Snapshot{OverageInUse: true, Primary: usage.Window{UsedPercent: 1, WindowMinutes: 300}}, wantAuthorization: poolSeat, wantPoolLeases: 1},
		{name: "exhausted caller login moves to the pool", ownSeatFirst: true, ownOverageBeforePool: true, observed: &usage.Snapshot{Primary: usage.Window{UsedPercent: 1, WindowMinutes: 300}}, wantAuthorization: poolSeat, wantPoolLeases: 1},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			var authorizations []string
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				authorizations = append(authorizations, r.Header.Get("Authorization"))
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, `{"id":"synthetic-message","type":"message","role":"assistant","model":"`+catalog.ModelIDClaudeOpus48.String()+`","content":[{"type":"text","text":"answer"}],"stop_reason":"end_turn","usage":{"input_tokens":11,"output_tokens":7}}`)
			}))
			defer upstream.Close()
			leaser := &scriptedSubscriptionLeaser{leases: []subscriptions.Lease{{AccountID: poolSeat, AccessToken: poolSeat}}}
			now := time.Now()
			observer := usage.NewObserver([]byte("synthetic-salt"), time.Hour, func() time.Time { return now })
			if scenario.observed != nil {
				snapshot := *scenario.observed
				snapshot.ObservedAt = now
				snapshot.Primary.ObservedAt = now
				observer.Record(observer.Key([]byte(parityAnthropicToken)), snapshot)
			}
			client := anthropic.NewClient("synthetic-paid-key", upstream.URL)
			service := withHMMPorts(NewService(staticRouter{decision: router.Decision{Provider: providers.ProviderAnthropic, Model: catalog.ModelIDClaudeOpus48.String()}}, dispatch.NewClients(map[string]providers.Client{providers.ProviderAnthropic: client}), nil, nil, false, providers.ProviderAnthropic, catalog.ModelIDClaudeOpus48.String(), nil)).
				WithManagedSubscriptions(leaser).
				WithDeploymentKeyedProviders(map[string]struct{}{providers.ProviderAnthropic: {}}).
				WithUsageObserver(observer)
			service.engine.SetRetrySleep(noopSleep)
			ctx := managedSubscriptionTestContext()
			if !scenario.personalPlan {
				ctx = enterpriseOnlyContext(ctx)
			}
			ctx = withOwnSeatFlags(ctx, scenario.ownSeatFirst, scenario.ownOverageBeforePool)
			ctx = context.WithValue(ctx, requestcontext.InstallationIDContextKey{}, "11111111-1111-1111-1111-111111111111")
			body := `{"model":"auto","max_tokens":1024,"messages":[{"role":"user","content":"inspect synthetic input"}]}`
			request := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/v1/messages", strings.NewReader(body))
			request.Header.Set("Authorization", "Bearer "+parityAnthropicToken)
			recorder := httptest.NewRecorder()

			err := service.ProxyMessages(ctx, []byte(body), recorder, request)

			require.NoError(t, err)
			require.Equal(t, http.StatusOK, recorder.Code)
			require.Equal(t, []string{"Bearer " + scenario.wantAuthorization}, authorizations)
			require.Len(t, leaser.providers, scenario.wantPoolLeases)
		})
	}
}

func withOwnSeatFlags(ctx context.Context, ownSeatFirst, ownOverageBeforePool bool) context.Context {
	overrides, _ := flags.OverridesFromContext(ctx)
	overrides.Bools = maps.Clone(overrides.Bools)
	if overrides.Bools == nil {
		overrides.Bools = make(map[flags.Key]bool)
	}
	overrides.Bools[flags.KeyOwnSeatFirst] = ownSeatFirst
	overrides.Bools[flags.KeyOwnOverageBeforePool] = ownOverageBeforePool
	return flags.WithOverrides(ctx, overrides)
}
