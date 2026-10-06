package proxy

import (
	"context"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	coltracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	tracev1 "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
	"weave-os/router/internal/auth"
	"weave-os/router/internal/observability/otel"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/providers/openai"
	"weave-os/router/internal/router"
	"weave-os/router/internal/subscriptions"
)

func TestVerificationSafeSubscriptionIngressConformance(t *testing.T) {
	fixtures := []struct {
		name, path, body string
		run              func(*Service, context.Context, []byte, http.ResponseWriter, *http.Request) error
	}{
		{"messages", "/v1/messages", `{"model":"auto","max_tokens":2048,"thinking":{"type":"enabled","budget_tokens":8192},"messages":[{"role":"user","content":"synthetic input"}],"tools":[{"name":"read_file","input_schema":{"type":"object","properties":{"path":{"type":"string"}}}}]}`, (*Service).ProxyMessages},
		{"chat", "/v1/chat/completions", `{"model":"auto","max_tokens":2048,"reasoning_effort":"medium","messages":[{"role":"user","content":"synthetic input"}],"tools":[{"type":"function","function":{"name":"read_file","parameters":{"type":"object","properties":{"path":{"type":"string"}}}}}],"response_format":{"type":"json_schema","json_schema":{"name":"answer","strict":true,"schema":{"type":"object","properties":{"answer":{"type":"string"}},"required":["answer"],"additionalProperties":false}}}}`, (*Service).ProxyOpenAIChatCompletion},
		{"responses", "/v1/responses", `{"model":"auto","reasoning":{"effort":"medium"},"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"synthetic input"}]}],"tools":[{"type":"function","name":"read_file","parameters":{"type":"object","properties":{"path":{"type":"string"}},"required":["path"],"additionalProperties":false},"strict":true}],"text":{"format":{"type":"json_schema","name":"answer","strict":true,"schema":{"type":"object","properties":{"answer":{"type":"string"}},"required":["answer"],"additionalProperties":false}}}}`, (*Service).ProxyOpenAIResponses},
	}
	for _, fixture := range fixtures {
		for _, stream := range []bool{false, true} {
			t.Run(fixture.name+map[bool]string{false: "/nonstream", true: "/stream"}[stream], func(t *testing.T) {
				var spanMu sync.Mutex
				var upstreamSpans []*tracev1.Span
				collector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path == "/v1/traces" {
						body, _ := io.ReadAll(r.Body)
						var exported coltracepb.ExportTraceServiceRequest
						require.NoError(t, proto.Unmarshal(body, &exported))
						spanMu.Lock()
						for _, resource := range exported.ResourceSpans {
							for _, scope := range resource.ScopeSpans {
								for _, span := range scope.Spans {
									if span.Name == "router.upstream" {
										upstreamSpans = append(upstreamSpans, span)
									}
								}
							}
						}
						spanMu.Unlock()
					}
					w.WriteHeader(http.StatusOK)
				}))
				defer collector.Close()
				emitter, err := otel.NewEmitter(otel.EmitterConfig{Endpoint: collector.URL, Workers: 1, QueueSize: 100, BatchSize: 1, FlushInterval: time.Millisecond})
				require.NoError(t, err)
				defer emitter.Shutdown(context.Background())
				var sent []byte
				var bearer, path, providerAccount string
				var gatewayMu sync.Mutex
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					requestBody, _ := io.ReadAll(r.Body)
					gatewayMu.Lock()
					bearer = r.Header.Get("Authorization")
					path = r.URL.Path
					providerAccount = r.Header.Get("ChatGPT-Account-ID")
					authorized := bearer == "Bearer synthetic-included-token"
					if authorized {
						sent = requestBody
					}
					gatewayMu.Unlock()
					if !authorized {
						w.WriteHeader(http.StatusForbidden)
						return
					}
					w.Header().Set("Content-Type", "text/event-stream")
					for _, frame := range []string{
						`{"type":"response.output_text.delta","output_index":0,"delta":"synthetic answer"}`,
						`{"type":"response.output_item.added","output_index":1,"item":{"type":"function_call","call_id":"synthetic-call","name":"read_file"}}`,
						`{"type":"response.function_call_arguments.done","output_index":1,"arguments":"{\"path\":\"fixture.go\"}"}`,
						`{"type":"response.output_item.done","output_index":1,"item":{"type":"function_call","call_id":"synthetic-call","name":"read_file","arguments":"{\"path\":\"fixture.go\"}"}}`,
						`{"type":"response.completed","response":{"id":"synthetic-response","status":"completed","output":[{"type":"function_call","call_id":"synthetic-call","name":"read_file","arguments":"{\"path\":\"fixture.go\"}"}],"usage":{"input_tokens":11,"output_tokens":7}}}`,
					} {
						_, _ = io.WriteString(w, "data: "+frame+"\n\n")
					}
				}))
				defer server.Close()
				client := &includedOnlySyntheticClient{Client: openai.NewClient("", server.URL)}
				client.SetCodexBaseURL(server.URL)
				leaser := &scriptedSubscriptionLeaser{leases: []subscriptions.Lease{{AccountID: "10000000-0000-4000-8000-000000000001", OwnerID: "10000000-0000-4000-8000-000000000002", Tier: auth.SubscriptionTierShared, AccessToken: "synthetic-included-token", ProviderAccount: "synthetic-provider-account"}}}
				svc := NewService(staticRouter{decision: router.Decision{Provider: providers.ProviderOpenAI, Model: codexCoveredModel, Reason: "test"}}, map[string]providers.Client{providers.ProviderOpenAI: client}, emitter, false, nil, nil, false, providers.ProviderOpenAI, codexCoveredModel, nil).WithManagedSubscriptions(leaser)
				body := fixture.body[:len(fixture.body)-1] + `,"stream":` + map[bool]string{false: "false", true: "true"}[stream] + `}`
				ctx := context.WithValue(managedSubscriptionContext(auth.SubscriptionProviderCodex), InstallationIDContextKey{}, "11111111-1111-1111-1111-111111111111")
				ctx = context.WithValue(ctx, ExternalIDContextKey{}, "org_subscription_verification")
				ctx = context.WithValue(ctx, APIKeyIDContextKey{}, "10000000-0000-4000-8000-000000000003")
				ctx = context.WithValue(ctx, ClientIdentityContextKey{}, ClientIdentity{ClientApp: ClientAppCodex})
				rec := httptest.NewRecorder()
				err = fixture.run(svc, ctx, []byte(body), rec, httptest.NewRequest(http.MethodPost, fixture.path, strings.NewReader(body)))
				require.NoError(t, err)
				gatewayMu.Lock()
				defer gatewayMu.Unlock()
				require.Equal(t, "/responses", path)
				require.Equal(t, "Bearer synthetic-included-token", bearer)
				require.Equal(t, "synthetic-provider-account", providerAccount)
				require.Equal(t, "read_file", gjson.GetBytes(sent, "tools.0.name").String())
				require.Equal(t, "medium", gjson.GetBytes(sent, "reasoning.effort").String())
				if fixture.name != "messages" {
					require.Equal(t, "json_schema", gjson.GetBytes(sent, "text.format.type").String())
					require.Equal(t, "string", gjson.GetBytes(sent, "text.format.schema.properties.answer.type").String())
				}
				require.Contains(t, rec.Body.String(), "read_file")
				require.Contains(t, rec.Body.String(), "fixture.go")
				winner := ctx.Value(ManagedSubscriptionUsageContextKey{}).(*ManagedSubscriptionUsage)
				require.True(t, winner.Served)
				require.Equal(t, "10000000-0000-4000-8000-000000000001", winner.SubscriptionAccountID)
				require.Equal(t, "10000000-0000-4000-8000-000000000002", winner.SubscriptionOwnerID)
				require.Equal(t, auth.SubscriptionTierShared, winner.SubscriptionTier)
				require.False(t, winner.OverageInUse)
				require.NoError(t, emitter.Shutdown(context.Background()))
				spanMu.Lock()
				defer spanMu.Unlock()
				require.NotEmpty(t, upstreamSpans, "actual exported provider-attempt spans required")
				if exportPath := os.Getenv("SUBSCRIPTION_VERIFICATION_SPAN_FILE"); exportPath != "" && fixture.name == "chat" && !stream {
					exported, err := protojson.Marshal(upstreamSpans[len(upstreamSpans)-1])
					require.NoError(t, err)
					require.NoError(t, os.WriteFile(exportPath, exported, 0600))
				}

				attrs := map[string]string{}
				for _, attribute := range upstreamSpans[len(upstreamSpans)-1].Attributes {
					attrs[attribute.Key] = attribute.Value.GetStringValue()
				}
				require.Equal(t, "10000000-0000-4000-8000-000000000001", attrs["subscription.account_id"])
				require.Equal(t, "10000000-0000-4000-8000-000000000002", attrs["subscription.owner_id"])
				require.Equal(t, "shared", attrs["subscription.tier"])
				require.Equal(t, "org_subscription_verification", attrs["external_id"])
				statusFound := false
				for _, attribute := range upstreamSpans[len(upstreamSpans)-1].Attributes {
					if attribute.Key == "upstream.status_code" {
						statusFound = true
						require.EqualValues(t, 200, attribute.Value.GetIntValue())
					}
				}
				require.True(t, statusFound, "upstream span must report upstream.status_code")

			})
		}
	}
}
