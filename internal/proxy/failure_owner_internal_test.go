package proxy

import (
	"context"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/router/hmm"
	"weave-os/router/internal/translate"
)

// declaredConstsOfType lists the package-level constant names declared with
// typeName in this package, so a newly added value cannot skip the mapping.
func declaredConstsOfType(t *testing.T, typeName string) []string {
	t.Helper()
	files, err := filepath.Glob("*.go")
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
				value := spec.(*ast.ValueSpec)
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
	names := declaredConstsOfType(t, "DispatchErrorKind")
	// iota-declared: index i in declaration order is the kind's value.
	for i, name := range names {
		kind := DispatchErrorKind(i)
		switch kind {
		case DispatchErrorNone, DispatchErrorUpstreamStatus:
			continue
		}
		assert.NotEqual(t, FailureOwnerUnknown, dispatchErrorKindOwner(kind), "%s has no failure owner", name)
	}
	assert.Equal(t, FailureOwnerUnknown, dispatchErrorKindOwner(DispatchErrorKind(len(names))))
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
	for _, name := range declaredConstsOfType(t, "streamFailureClass") {
		class, ok := classes[name]
		require.True(t, ok, "%s is not covered by this test; add it and give it an owner", name)
		owner := streamFailureOwner(class)
		if class == streamFailureOther {
			assert.Equal(t, FailureOwnerUnknown, owner)
			continue
		}
		assert.NotEqual(t, FailureOwnerUnknown, owner, "%s has no failure owner", name)
	}
	assert.Equal(t, FailureOwnerProvider, streamFailureOwner(streamFailureIdleWatchdog))
	assert.Equal(t, FailureOwnerClient, streamFailureOwner(streamFailureClientCanceled))
}

func TestClassifyFailureOwner_TranslatedVersusUntranslated400(t *testing.T) {
	rejected := &providers.UpstreamErrorResponse{Status: http.StatusBadRequest, Body: []byte(`{"error":{"message":"bad tool schema"}}`)}
	assert.Equal(t, FailureOwnerRouter, classifyFailureOwner(failureOwnerInput{Err: rejected, BodyTranslated: true}))
	assert.Equal(t, FailureOwnerProvider, classifyFailureOwner(failureOwnerInput{Err: rejected}))
	unprocessable := &providers.UpstreamStatusError{Status: http.StatusUnprocessableEntity}
	assert.Equal(t, FailureOwnerRouter, classifyFailureOwner(failureOwnerInput{Err: unprocessable, BodyTranslated: true}))
	assert.Equal(t, FailureOwnerProvider, classifyFailureOwner(failureOwnerInput{Err: unprocessable}))
}

func TestClassifyFailureOwner_Cases(t *testing.T) {
	cases := []struct {
		name string
		in   failureOwnerInput
		want FailureOwner
	}{
		{"nil", failureOwnerInput{}, ""},
		{"overloaded 529", failureOwnerInput{Err: &providers.UpstreamErrorResponse{Status: 529}}, FailureOwnerProvider},
		{"rate limited", failureOwnerInput{Err: &providers.UpstreamStatusError{Status: http.StatusTooManyRequests}}, FailureOwnerProvider},
		{"5xx", failureOwnerInput{Err: &providers.UpstreamErrorResponse{Status: http.StatusBadGateway}}, FailureOwnerProvider},
		{"watchdog wrapped in synthetic 502", failureOwnerInput{Err: &providers.UpstreamStatusError{Status: http.StatusBadGateway, Cause: providers.ErrUpstreamIdleTimeout}, BodyTranslated: true}, FailureOwnerProvider},
		{"stalled stream", failureOwnerInput{Err: fmt.Errorf("read: %w", providers.ErrUpstreamOutputStall)}, FailureOwnerProvider},
		{"client canceled", failureOwnerInput{Err: fmt.Errorf("proxy: %w", context.Canceled)}, FailureOwnerClient},
		{"weave key 401", failureOwnerInput{Err: &providers.UpstreamErrorResponse{Status: http.StatusUnauthorized}}, FailureOwnerRouter},
		{"byok 401", failureOwnerInput{Err: &providers.UpstreamErrorResponse{Status: http.StatusUnauthorized}, CustomerCredential: true}, FailureOwnerClient},
		{"upstream 404", failureOwnerInput{Err: &providers.UpstreamErrorResponse{Status: http.StatusNotFound}}, FailureOwnerRouter},
		{"status argument wins over chain", failureOwnerInput{Err: errors.New("boom"), Status: http.StatusServiceUnavailable}, FailureOwnerProvider},
		{"hmm outage", failureOwnerInput{Err: fmt.Errorf("route: %w", hmm.ErrHMMUnavailable)}, FailureOwnerRouter},
		{"handoff unavailable", failureOwnerInput{Err: ErrHandoffUnavailable}, FailureOwnerRouter},
		{"handoff invalid", failureOwnerInput{Err: ErrHandoffInvalid}, FailureOwnerClient},
		{"context overflow", failureOwnerInput{Err: &providers.UpstreamErrorResponse{Status: http.StatusBadRequest, Body: []byte(`{"type":"error","error":{"type":"invalid_request_error","message":"prompt is too long: 250000 tokens > 200000 maximum"}}`)}, BodyTranslated: true}, FailureOwnerClient},
		{"subscription pool exhausted", failureOwnerInput{Err: fmt.Errorf("dispatch: %w", ErrSubscriptionPoolExhausted)}, FailureOwnerRouter},
		{"custom tool arguments untranslatable", failureOwnerInput{Err: fmt.Errorf("write: %w", translate.ErrCustomToolArguments), BodyTranslated: true}, FailureOwnerRouter},
		{"unmatched", failureOwnerInput{Err: errors.New("mystery")}, FailureOwnerUnknown},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, classifyFailureOwner(tc.in))
		})
	}
}

func TestTurnFailureOwner(t *testing.T) {
	assert.Equal(t, FailureOwnerProvider, turnFailureOwner(TurnErrorRefusal))
	assert.Equal(t, FailureOwnerRouter, turnFailureOwner(TurnErrorRoutingRefused))
	assert.Equal(t, FailureOwner(""), turnFailureOwner(""))
	assert.Equal(t, FailureOwner(""), turnFailureOwner(TurnErrorMaxTokens))
}
