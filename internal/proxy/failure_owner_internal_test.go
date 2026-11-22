package proxy

import (
	"context"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/conversation"
	"weave-os/router/internal/credentials"
	"weave-os/router/internal/ingress"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/router/hmm"
	"weave-os/router/internal/telemetry/turnrecord"
	"weave-os/router/internal/translate"
)

// declaredConstsOfType lists the package-level constant names declared with
// typeName in dir, so a newly added value cannot skip the mapping.
func declaredConstsOfType(t *testing.T, dir, typeName string) []string {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(dir, "*.go"))
	require.NoError(t, err)
	fset := token.NewFileSet()
	var names []string
	for _, file := range files {
		parsed, err := parser.ParseFile(fset, file, nil, 0)
		require.NoError(t, err)
		for _, decl := range parsed.Decls {
			gen, ok := decl.(*ast.GenDecl)
			if !ok || gen.Tok != token.CONST {
				continue
			}
			current := ""
			for _, spec := range gen.Specs {
				value := mustAs[*ast.ValueSpec](t, spec)
				if ident, ok := value.Type.(*ast.Ident); ok {
					current = ident.Name
				} else if value.Type != nil || len(value.Values) > 0 {
					current = ""
				}
				if current != typeName {
					continue
				}
				for _, name := range value.Names {
					names = append(names, name.Name)
				}
			}
		}
	}
	require.NotEmpty(t, names, "no %s constants found", typeName)
	return names
}

func TestDispatchErrorKindOwner_EveryKindLabeled(t *testing.T) {
	names := declaredConstsOfType(t, "../ingress", "DispatchErrorKind")
	// iota-declared: index i in declaration order is the kind's value.
	for i, name := range names {
		if name == "_" {
			continue
		}
		kind := ingress.DispatchErrorKind(i)
		switch kind {
		case ingress.DispatchErrorNone, ingress.DispatchErrorUpstreamStatus:
			continue
		default:
		}
		assert.NotEqual(t, turnrecord.FailureOwnerUnknown, dispatchErrorKindOwner(kind), "%s has no failure owner", name)
	}
	assert.Equal(t, turnrecord.FailureOwnerUnknown, dispatchErrorKindOwner(ingress.DispatchErrorKind(len(names))))
}

func TestStreamFailureOwner_EveryClassLabeled(t *testing.T) {
	classes := map[string]streamFailureClass{
		"streamFailureUpstreamEOF": streamFailureUpstreamEOF, "streamFailureUpstreamReset": streamFailureUpstreamReset,
		"streamFailureIdleWatchdog": streamFailureIdleWatchdog, "streamFailureOutputStallWatchdog": streamFailureOutputStallWatchdog,
		"streamFailureSlowThroughputWatchdog": streamFailureSlowThroughputWatchdog, "streamFailureUpstreamErrorFrame": streamFailureUpstreamErrorFrame,
		"streamFailureUpstreamIncomplete": streamFailureUpstreamIncomplete, "streamFailureUpstreamEmpty": streamFailureUpstreamEmpty,
		"streamFailureUpstreamTimeout": streamFailureUpstreamTimeout, "streamFailureClientCanceled": streamFailureClientCanceled,
		"streamFailureDeadline": streamFailureDeadline, "streamFailureOther": streamFailureOther,
	}
	for _, name := range declaredConstsOfType(t, ".", "streamFailureClass") {
		class, ok := classes[name]
		require.True(t, ok, "%s is not covered by this test; add it and give it an owner", name)
		owner := streamFailureOwner(class)
		if class == streamFailureOther {
			assert.Equal(t, turnrecord.FailureOwnerUnknown, owner)
			continue
		}
		assert.NotEqual(t, turnrecord.FailureOwnerUnknown, owner, "%s has no failure owner", name)
	}
	assert.Equal(t, turnrecord.FailureOwnerProvider, streamFailureOwner(streamFailureIdleWatchdog))
	assert.Equal(t, turnrecord.FailureOwnerClient, streamFailureOwner(streamFailureClientCanceled))
}

func TestClassifyFailureOwner_TranslatedVersusUntranslated400(t *testing.T) {
	rejected := &providers.UpstreamErrorResponse{Status: http.StatusBadRequest, Body: []byte(`{"error":{"message":"bad tool schema"}}`)}
	assert.Equal(t, turnrecord.FailureOwnerRouter, classifyFailureOwner(failureOwnerInput{Err: rejected, BodyTranslated: true}))
	assert.Equal(t, turnrecord.FailureOwnerProvider, classifyFailureOwner(failureOwnerInput{Err: rejected}))
	unprocessable := &providers.UpstreamStatusError{Status: http.StatusUnprocessableEntity}
	assert.Equal(t, turnrecord.FailureOwnerRouter, classifyFailureOwner(failureOwnerInput{Err: unprocessable, BodyTranslated: true}))
	assert.Equal(t, turnrecord.FailureOwnerProvider, classifyFailureOwner(failureOwnerInput{Err: unprocessable}))
}

func TestClassifyFailureOwner_Cases(t *testing.T) {
	cases := []struct {
		name string
		in   failureOwnerInput
		want turnrecord.FailureOwner
	}{
		{"nil", failureOwnerInput{}, ""},
		{"overloaded 529", failureOwnerInput{Err: &providers.UpstreamErrorResponse{Status: 529}}, turnrecord.FailureOwnerProvider},
		{"rate limited", failureOwnerInput{Err: &providers.UpstreamStatusError{Status: http.StatusTooManyRequests}}, turnrecord.FailureOwnerProvider},
		{"5xx", failureOwnerInput{Err: &providers.UpstreamErrorResponse{Status: http.StatusBadGateway}}, turnrecord.FailureOwnerProvider},
		{"watchdog wrapped in synthetic 502", failureOwnerInput{Err: &providers.UpstreamStatusError{Status: http.StatusBadGateway, Cause: providers.ErrUpstreamIdleTimeout}, BodyTranslated: true}, turnrecord.FailureOwnerProvider},
		{"stalled stream", failureOwnerInput{Err: fmt.Errorf("read: %w", providers.ErrUpstreamOutputStall)}, turnrecord.FailureOwnerProvider},
		{"client canceled", failureOwnerInput{Err: fmt.Errorf("proxy: %w", context.Canceled)}, turnrecord.FailureOwnerClient},
		{"weave key 401", failureOwnerInput{Err: &providers.UpstreamErrorResponse{Status: http.StatusUnauthorized}}, turnrecord.FailureOwnerRouter},
		{"own Codex login rejected", failureOwnerInput{Err: attributeCodexLoginRejection(&providers.UpstreamErrorResponse{Status: http.StatusUnauthorized})}, turnrecord.FailureOwnerClient},
		{"byok 401", failureOwnerInput{Err: &providers.UpstreamErrorResponse{Status: http.StatusUnauthorized}, CustomerCredential: true}, turnrecord.FailureOwnerClient},
		{"upstream 404", failureOwnerInput{Err: &providers.UpstreamErrorResponse{Status: http.StatusNotFound}}, turnrecord.FailureOwnerRouter},
		{"status argument wins over chain", failureOwnerInput{Err: errors.New("boom"), Status: http.StatusServiceUnavailable}, turnrecord.FailureOwnerProvider},
		{"hmm outage", failureOwnerInput{Err: fmt.Errorf("route: %w", hmm.ErrHMMUnavailable)}, turnrecord.FailureOwnerRouter},
		{"handoff unavailable", failureOwnerInput{Err: conversation.ErrHandoffUnavailable}, turnrecord.FailureOwnerRouter},
		{"handoff invalid", failureOwnerInput{Err: conversation.ErrHandoffInvalid}, turnrecord.FailureOwnerClient},
		{"context overflow", failureOwnerInput{Err: &providers.UpstreamErrorResponse{Status: http.StatusBadRequest, Body: []byte(`{"type":"error","error":{"type":"invalid_request_error","message":"prompt is too long: 250000 tokens > 200000 maximum"}}`)}, BodyTranslated: true}, turnrecord.FailureOwnerClient},
		{"subscription pool exhausted", failureOwnerInput{Err: fmt.Errorf("dispatch: %w", credentials.ErrSubscriptionPoolExhausted)}, turnrecord.FailureOwnerRouter},
		{"subscription pool exhausted by an upstream 429 on every seat", failureOwnerInput{Err: errors.Join(credentials.ErrSubscriptionPoolExhausted, &providers.UpstreamErrorResponse{Status: http.StatusTooManyRequests, Body: []byte(`{"detail":"Rate limit exceeded"}`)})}, turnrecord.FailureOwnerProvider},
		{"subscription pool exhausted after an upstream 401 stays router-owned", failureOwnerInput{Err: errors.Join(credentials.ErrSubscriptionPoolExhausted, &providers.UpstreamErrorResponse{Status: http.StatusUnauthorized})}, turnrecord.FailureOwnerRouter},
		{"pool refused because every seat is in a provider rate-limit cooldown", failureOwnerInput{Err: fmt.Errorf("dispatch: %w", credentials.ErrSubscriptionPoolThrottled)}, turnrecord.FailureOwnerProvider},
		{"pool unavailable for a non-throttle reason stays router-owned", failureOwnerInput{Err: credentials.ErrSubscriptionPoolUnavailable}, turnrecord.FailureOwnerRouter},
		{"customer key over its own usage limit on a translated body", failureOwnerInput{Err: &providers.UpstreamErrorResponse{Status: http.StatusBadRequest, Rejection: providers.UpstreamRejection{Kind: providers.UpstreamRejectionCredential}}, BodyTranslated: true, CustomerCredential: true}, turnrecord.FailureOwnerClient},
		{"deployment key over its usage limit stays router-owned", failureOwnerInput{Err: &providers.UpstreamErrorResponse{Status: http.StatusBadRequest, Rejection: providers.UpstreamRejection{Kind: providers.UpstreamRejectionCredential}}, BodyTranslated: true}, turnrecord.FailureOwnerRouter},
		{"translated body rejected for a field the client sent unchanged", failureOwnerInput{Err: &providers.UpstreamErrorResponse{Status: http.StatusBadRequest}, BodyTranslated: true, RejectedFieldUnchanged: true}, turnrecord.FailureOwnerClient},
		{"spend-cap lane refusal outranks the last seat's 429", failureOwnerInput{Err: errors.Join(credentials.ErrSpendCapSubscriptionUnavailable, credentials.ErrSubscriptionPoolExhausted, &providers.UpstreamErrorResponse{Status: http.StatusTooManyRequests})}, turnrecord.FailureOwnerClient},
		{"responses passthrough cannot switch format", failureOwnerInput{Err: errResponsesPassthroughStuck}, turnrecord.FailureOwnerRouter},
		{"malformed custom tool arguments", failureOwnerInput{Err: fmt.Errorf("write: %w", translate.ErrCustomToolArguments), BodyTranslated: true}, turnrecord.FailureOwnerProvider},
		{"http/2 connection lost to the provider", failureOwnerInput{Err: http2LostPing()}, turnrecord.FailureOwnerProvider},
		{"http/2 stream reset by the provider", failureOwnerInput{Err: http2PeerStreamReset()}, turnrecord.FailureOwnerProvider},
		{"unmatched", failureOwnerInput{Err: errors.New("mystery")}, turnrecord.FailureOwnerUnknown},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, classifyFailureOwner(tc.in))
		})
	}
}

func TestTurnFailureOwner(t *testing.T) {
	assert.Equal(t, turnrecord.FailureOwnerProvider, turnFailureOwner(turnrecord.TurnErrorRefusal))
	assert.Equal(t, turnrecord.FailureOwnerRouter, turnFailureOwner(turnrecord.TurnErrorRoutingRefused))
	assert.Equal(t, turnrecord.FailureOwner(""), turnFailureOwner(""))
	assert.Equal(t, turnrecord.FailureOwner(""), turnFailureOwner(turnrecord.TurnErrorMaxTokens))
}

// A finish reason is only recorded once the upstream's own terminal arrived,
// so a missing terminal after one was lost inside the router, not upstream.
func TestTurnFailureOwnerAfterUpstreamFinish(t *testing.T) {
	cut := fmt.Errorf("write: %w", translate.ErrStreamIncomplete)
	cases := []struct {
		name   string
		err    error
		finish string
		want   turnrecord.FailureOwner
	}{
		{"cut after upstream stop", cut, "stop", turnrecord.FailureOwnerRouter},
		{"cut after upstream tool call", cut, "tool_calls", turnrecord.FailureOwnerRouter},
		{"empty stream after upstream finish", translate.ErrStreamEmpty, "end_turn", turnrecord.FailureOwnerRouter},
		{"cut before any upstream finish", cut, "", turnrecord.FailureOwnerProvider},
		{"rate limited with a finish reason", &providers.UpstreamStatusError{Status: http.StatusTooManyRequests}, "stop", turnrecord.FailureOwnerProvider},
		{"served turn", nil, "stop", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			class := classifyTurnError(tc.err, tc.finish, 0)
			assert.Equal(t, tc.want, turnFailureOwnerAfterFinish(context.Background(), tc.err, class, tc.finish))
		})
	}
}

func TestLeaveResponsesPassthrough(t *testing.T) {
	clean := translate.NewResponsesWriter(httptest.NewRecorder(), "synthetic")
	clean.SetPassthrough()
	require.NoError(t, (responseProtocolSwitch{writer: clean}).leavePassthrough())
	assert.False(t, clean.Passthrough(), "a writer with nothing forwarded translates the next attempt")

	forwarded := translate.NewResponsesWriter(httptest.NewRecorder(), "synthetic")
	forwarded.SetPassthrough()
	forwarded.Header().Set("Content-Type", "text/event-stream")
	_, err := forwarded.Write([]byte(`data: {"type":"response.created","response":{"id":"resp_synthetic"}}` + "\n\n"))
	require.NoError(t, err)
	require.ErrorIs(t, (responseProtocolSwitch{writer: forwarded}).leavePassthrough(), errResponsesPassthroughStuck)
	assert.True(t, forwarded.Passthrough())
}
