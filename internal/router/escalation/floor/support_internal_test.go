package floor

import (
	"bytes"
	"context"
	"net/http"

	"github.com/google/uuid"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/escalation"
	"weave-os/router/internal/router/llmescalation"
	"weave-os/router/internal/router/sessionpin"
	"weave-os/router/internal/router/turntype"
	"weave-os/router/internal/translate"
)

const testAPIKeyID = "test-key"

type nativeResponsesBodyContextKey struct{}

type codexClientContextKey struct{}

type testResult struct {
	Strategy               router.Strategy
	InstallationID         uuid.UUID
	TurnType               turntype.TurnType
	SessionKey             [sessionpin.SessionKeyLen]byte
	Decision               router.Decision
	EscalationShadowMarked bool
	EscalationScope        [32]byte
	EscalationOrdinal      int64
	llmEscalation          *llmTurn
	xgb                    *xgbTurn
}

type testCapture struct {
	body     bytes.Buffer
	maxBytes int
	header   http.Header
}

func newCaptureWriter() *testCapture {
	return &testCapture{maxBytes: HistoryMaxBytes, header: make(http.Header)}
}

func (c *testCapture) Header() http.Header { return c.header }

func (c *testCapture) WriteHeader(int) {}

func (c *testCapture) Write(p []byte) (int, error) {
	c.body.Write(p)
	return len(p), nil
}

func (c *testCapture) captured() ([]byte, bool) {
	if c == nil {
		return nil, false
	}
	if c.body.Len() > c.maxBytes {
		return c.body.Bytes()[:c.maxBytes], true
	}
	return c.body.Bytes(), false
}

type testService struct{ *Helper }

func newTestService() *testService {
	return &testService{Helper: New(func(context.Context, string, string, llmescalation.Mode, llmescalation.Job, llmescalation.Judgment, llmescalation.FailureCode) {
	}, func(reason string) string { return reason })}
}

func (s *testService) WithEscalation(store escalation.Store, observer escalation.Observer) *testService {
	s.WithXGB(store, observer)
	return s
}

func (s *testService) WithLLMEscalation(store llmescalation.Store, judge llmescalation.Judge) *testService {
	s.WithLLM(store, judge)
	return s
}

func (s *testService) WithQwenEscalation(judge llmescalation.Judge) *testService {
	s.WithQwen(judge)
	return s
}

func (s *testService) WithEscalationConfiguration(_ llmescalation.ConfigurationStore, _ func(string), activeEnabled bool) *testService {
	s.WithSwitchyardActive(activeEnabled)
	return s
}

func testFacts(ctx context.Context, env *translate.RequestEnvelope, res *testResult, apiKeyID string) Facts {
	original, isNativeResponses := ctx.Value(nativeResponsesBodyContextKey{}).([]byte)
	identity := apiKeyID
	if serving, managed := requestcontext.ServingIdentityFromContext(ctx); managed && serving.CredentialIdentity != "" {
		identity = serving.CredentialIdentity
	}
	codex, _ := ctx.Value(codexClientContextKey{}).(bool)
	return Facts{Strategy: res.Strategy, InstallationID: res.InstallationID, SessionKey: res.SessionKey, TurnType: res.TurnType, Envelope: env, NativeResponsesBody: original, NativeResponses: isNativeResponses, APIKeyID: apiKeyID, CredentialIdentity: identity, CodexClient: codex}
}

func (s *testService) beginEscalation(ctx context.Context, env *translate.RequestEnvelope, req router.Request, res *testResult) *xgbTurn {
	return s.beginXGB(ctx, testFacts(ctx, env, res, testAPIKeyID), req)
}

func (s *testService) beginLLMEscalation(ctx context.Context, env *translate.RequestEnvelope, req router.Request, res *testResult) *llmTurn {
	return s.beginLLM(ctx, testFacts(ctx, env, res, testAPIKeyID), req)
}

func (s *testService) finishEscalation(ctx context.Context, turn *xgbTurn, res *testResult) error {
	committed, err := s.Finish(ctx, &Turn{xgb: turn}, res.Decision, "", nil)
	if err != nil {
		return err
	}
	res.EscalationShadowMarked, res.EscalationScope, res.EscalationOrdinal, res.xgb = committed.ShadowMarked, committed.Scope, committed.Ordinal, turn
	return nil
}

func (s *testService) recordEscalationOutcome(ctx context.Context, res testResult, proxyErr error, capture *testCapture, format translate.EscalationResponseFormat) {
	body, truncated := capture.captured()
	s.recordXGBOutcome(ctx, Completion{Turn: &Turn{xgb: res.xgb}, Committed: Committed{Scope: res.EscalationScope, Ordinal: res.EscalationOrdinal}, ProxyErr: proxyErr, Body: body, Truncated: truncated, Format: format})
}

func (s *testService) applyLLMEscalation(ctx context.Context, turn *llmTurn, decision router.Decision) error {
	return s.ApplyLLM(ctx, &Turn{llm: turn}, decision)
}

func (s *testService) completeLLMEscalation(ctx context.Context, res testResult, capture *testCapture, format translate.EscalationResponseFormat) {
	body, truncated := capture.captured()
	var served bool
	if targets, ok := ctx.Value(servedTargetsContextKey{}).([]string); ok {
		for _, target := range targets {
			served = served || target == res.Decision.Model
		}
	}
	s.completeLLM(ctx, Completion{Turn: &Turn{llm: res.llmEscalation}, Body: body, Truncated: truncated, Format: format, ServedTarget: served})
}

type servedTargetsContextKey struct{}
