package floor

import (
	"context"
	"time"

	"github.com/google/uuid"
	"weave-os/router/internal/flags"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/escalation"
	"weave-os/router/internal/router/llmescalation"
	"weave-os/router/internal/router/sessionpin"
	"weave-os/router/internal/router/turntype"
	"weave-os/router/internal/translate"
)

const (
	xgbTimeout            = 500 * time.Millisecond
	HistoryMaxBytes       = 1024 * 1024
	llmBookkeepingTimeout = 100 * time.Millisecond
	llmPinTier            = "escalation_switchyard_llm_v1"
	qwenPinTier           = "escalation_llm_escalation"
	xgbPinTier            = "escalation_xgb"
)

type JudgeRecorder func(ctx context.Context, installationID, requestID string, mode llmescalation.Mode, job llmescalation.Job, judgment llmescalation.Judgment, failure llmescalation.FailureCode)

type Helper struct {
	store                   escalation.Store
	observer                escalation.Observer
	llmStore                llmescalation.Store
	llmJudge                llmescalation.Judge
	qwenJudge               llmescalation.Judge
	slots                   chan struct{}
	shadowSlots             chan struct{}
	shadowDefault           flags.EscalationClassifier
	switchyardActiveEnabled bool
	recordJudge             JudgeRecorder
	redactReason            func(string) string
}

func New(recordJudge JudgeRecorder, redactReason func(string) string) *Helper {
	return &Helper{recordJudge: recordJudge, redactReason: redactReason}
}

func (h *Helper) WithXGB(store escalation.Store, observer escalation.Observer) {
	h.store = store
	h.observer = observer
}

func (h *Helper) WithLLM(store llmescalation.Store, judge llmescalation.Judge) {
	h.llmStore = store
	h.llmJudge = judge
	h.slots = make(chan struct{}, llmescalation.MaxWorkers)
	h.shadowSlots = make(chan struct{}, llmescalation.MaxShadowWorkers)
}

func (h *Helper) WithQwen(judge llmescalation.Judge) {
	h.qwenJudge = judge
	if h.slots == nil {
		h.slots = make(chan struct{}, llmescalation.MaxWorkers)
		h.shadowSlots = make(chan struct{}, llmescalation.MaxShadowWorkers)
	}
}

func (h *Helper) WithShadowDefault(classifier flags.EscalationClassifier) {
	h.shadowDefault = classifier
}

func (h *Helper) WithSwitchyardActive(enabled bool) {
	h.switchyardActiveEnabled = enabled
}

func (h *Helper) LLMStore() llmescalation.Store {
	if h == nil {
		return nil
	}
	return h.llmStore
}

func (h *Helper) SwitchyardJudgeReady() bool { return h != nil && h.llmJudge != nil }

func (h *Helper) QwenJudgeReady() bool { return h != nil && h.qwenJudge != nil }

func (h *Helper) SwitchyardActiveEnabled() bool { return h != nil && h.switchyardActiveEnabled }

func (h *Helper) selection(ctx context.Context) flags.EscalationConfig {
	return flags.EscalationWithDeploymentShadow(ctx, h.shadowDefault)
}

type Facts struct {
	Strategy            router.Strategy
	InstallationID      uuid.UUID
	SessionKey          [sessionpin.SessionKeyLen]byte
	TurnType            turntype.TurnType
	Envelope            *translate.RequestEnvelope
	NativeResponsesBody []byte
	NativeResponses     bool
	APIKeyID            string
	CredentialIdentity  string
	ExcludedProviders   []string
	CodexClient         bool
	RetainReason        bool
}

type Turn struct {
	xgb *xgbTurn
	llm *llmTurn
}

func (h *Helper) Begin(ctx context.Context, facts Facts, req router.Request) *Turn {
	if h == nil {
		return nil
	}
	xgb := h.beginXGB(ctx, facts, req)
	llm := h.beginLLM(ctx, facts, req)
	if xgb == nil && llm == nil {
		return nil
	}
	return &Turn{xgb: xgb, llm: llm}
}

func (t *Turn) Apply(req *router.Request) {
	if t == nil {
		return
	}
	if t.xgb != nil {
		req.Escalation = t.xgb.constraint()
	}
	if t.llm != nil && t.llm.active {
		req.Escalation = t.llm.constraint()
	}
}

func (t *Turn) PinTier() string {
	if t == nil || t.llm == nil || !t.llm.active {
		return xgbPinTier
	}
	if t.llm.session.Config.EffectiveClassifier() == flags.EscalationClassifierLLM {
		return qwenPinTier
	}
	return llmPinTier
}

func (t *Turn) HasXGB() bool { return t != nil && t.xgb != nil }

func (t *Turn) HasLLM() bool { return t != nil && t.llm != nil }

func (t *Turn) LLMActive() bool { return t.HasLLM() && t.llm.active }

func (t *Turn) LLMApplied() bool { return t.HasLLM() && t.llm.applied }

func (t *Turn) Ordinal() int64 {
	if !t.HasXGB() {
		return 0
	}
	return t.xgb.session.Ordinal
}

func RoutingApplied(decision router.Decision) bool {
	if decision.Metadata == nil || decision.Metadata.Escalation == nil {
		return false
	}
	intervention := decision.Metadata.Escalation
	return intervention.Constrained
}

type Committed struct {
	ShadowMarked bool
	Scope        [32]byte
	Ordinal      int64
}

type Completion struct {
	Turn         *Turn
	Committed    Committed
	ProxyErr     error
	Body         []byte
	Truncated    bool
	Format       translate.EscalationResponseFormat
	ServedTarget bool
}

func (h *Helper) Complete(ctx context.Context, completion Completion) {
	if h == nil {
		return
	}
	h.recordXGBOutcome(ctx, completion)
	h.completeLLM(ctx, completion)
}
