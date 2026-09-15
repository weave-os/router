// Package llmescalation defines independent asynchronous trajectory judging.
package llmescalation

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"time"

	"weave-os/router/internal/flags"
	"weave-os/router/internal/inference"
	"weave-os/router/internal/router/escalation"
)

// ErrStale identifies state from an expired or superseded session lifetime.
var ErrStale = errors.New("LLM escalation lifetime superseded")

const (
	Version            = string(flags.EscalationClassifierSwitchyard)
	SwitchyardRevision = "9c774d2ee6fcac818410419dab3578c3e7abee57"
	DefaultCadence     = 3
	MaxJudgeCalls      = 30
	JudgeTimeout       = 20 * time.Second
	JobLease           = 30 * time.Second
	MaxWorkers         = 16
)

// SystemPrompt and ResponseSchema are unmodified Apache-2.0 Switchyard assets.
//
//go:embed prompt.md
var SystemPrompt string

//go:embed schema.json
var ResponseSchema json.RawMessage

// Mode isolates active and shadow experiment state.
type Mode string

const (
	ModeOff    Mode = "off"
	ModeActive Mode = "active"
	ModeShadow Mode = "shadow"
)

// Config identifies the immutable experiment settings in a session scope.
type Config struct {
	Mode    Mode   `json:"mode"`
	Epoch   int    `json:"epoch"`
	Digest  string `json:"digest"`
	Cadence int    `json:"cadence"`
}

// JudgeRequest freezes the rendered transcript and operational attribution.
type JudgeRequest struct {
	Transcript  string
	RequestID   string
	OperationID string
}

// Judgment keeps unknown provider usage distinct from measured zero usage.
type Judgment struct {
	Escalate   bool            `json:"escalate"`
	Reason     string          `json:"reason"`
	Usage      inference.Usage `json:"usage"`
	CostUSD    float64         `json:"cost_usd"`
	CostKnown  bool            `json:"cost_known"`
	CostSource CostSource      `json:"cost_source"`
}

// CostSource identifies whether spend came from the provider or catalog pricing.
type CostSource string

const (
	CostSourceUnknown          CostSource = "unknown"
	CostSourceProviderReported CostSource = "provider_reported"
	CostSourceCatalogEstimate  CostSource = "catalog_estimate"
)

// Judge is implemented by the policy-authorized inference adapter.
type Judge interface {
	Judge(context.Context, JudgeRequest) (Judgment, error)
}

// JobStatus describes one paid inference checkpoint's lifecycle.
type JobStatus string

const (
	JobRunning   JobStatus = "running"
	JobCompleted JobStatus = "completed"
	JobFailed    JobStatus = "failed"
	JobSkipped   JobStatus = "skipped"
	JobStale     JobStatus = "stale"
	JobApplied   JobStatus = "applied"
)

// FailureCode is safe operational metadata, never an upstream response body.
type FailureCode string

const (
	FailureNone      FailureCode = ""
	FailureCapacity  FailureCode = "capacity"
	FailureCallLimit FailureCode = "call_limit"
	FailureInFlight  FailureCode = "in_flight"
	FailureStale     FailureCode = "stale"
	FailureTimeout   FailureCode = "timeout"
	FailureJudge     FailureCode = "judge_error"
	FailureInvalid   FailureCode = "invalid_response"
	FailureNoTarget  FailureCode = "no_eligible_target"
	FailureExpired   FailureCode = "lease_expired"
)

// Session stores cadence and the accepted floor independently of ordinary pins.
type Session struct {
	Scope                  [32]byte         `json:"scope"`
	Lifetime               string           `json:"lifetime"`
	InstallationID         string           `json:"installation_id"`
	Config                 Config           `json:"config"`
	InstructionFingerprint [32]byte         `json:"instruction_fingerprint"`
	Generation             int64            `json:"generation"`
	CompletedTurns         int64            `json:"completed_turns"`
	LatestCheckpoint       int64            `json:"latest_checkpoint"`
	JudgeCalls             int              `json:"judge_calls"`
	Floor                  escalation.Group `json:"floor"`
	Pending                *Job             `json:"pending,omitempty"`
	LastActivityAt         time.Time        `json:"last_activity_at"`
}

// Job is bounded audit metadata; neither transcript nor raw provider content is persisted.
type Job struct {
	ID               string      `json:"id"`
	Scope            [32]byte    `json:"scope"`
	Lifetime         string      `json:"lifetime"`
	Generation       int64       `json:"generation"`
	Checkpoint       int64       `json:"checkpoint"`
	RequestID        string      `json:"request_id"`
	Status           JobStatus   `json:"status"`
	Failure          FailureCode `json:"failure"`
	Judgment         *Judgment   `json:"judgment,omitempty"`
	CreatedAt        time.Time   `json:"created_at"`
	FinishedAt       *time.Time  `json:"finished_at,omitempty"`
	AppliedRequestID string      `json:"applied_request_id,omitempty"`
	AppliedTurn      *int64      `json:"applied_turn,omitempty"`
	Model            string      `json:"model"`
	Provider         string      `json:"provider"`
	Version          string      `json:"version"`
	PromptRevision   string      `json:"prompt_revision"`
	SchemaRevision   string      `json:"schema_revision"`
	RendererRevision string      `json:"renderer_revision"`
	ConfigDigest     string      `json:"config_digest"`
}

// StartRequest fences changes in user instruction before verdict consumption.
type StartRequest struct {
	Scope                  [32]byte
	InstallationID         string
	InstructionFingerprint [32]byte
	Config                 Config
}

// CompleteRequest counts one successfully delivered logical response exactly once.
type CompleteRequest struct {
	Session   Session
	Boundary  [32]byte
	RequestID string
	Capacity  bool
}

// Completion returns a newly claimed job only when inference should be launched.
type Completion struct {
	Session   Session
	Job       *Job
	Duplicate bool
}

// Summary aggregates content-free retained checkpoint outcomes.
type Summary struct {
	PositiveJudgments      int64 `json:"positive_judgments"`
	ActualInterventions    int64 `json:"actual_interventions"`
	ShadowInterventions    int64 `json:"shadow_interventions"`
	StaleResults           int64 `json:"stale_results"`
	Timeouts               int64 `json:"timeouts"`
	InvalidResponses       int64 `json:"invalid_responses"`
	CapacitySkips          int64 `json:"capacity_skips"`
	AttemptLimitExhaustion int64 `json:"attempt_limit_exhaustion"`
}

// ApplyRequest records only a routing constraint that was successfully applied.
type ApplyRequest struct {
	Session   Session
	JobID     string
	Floor     escalation.Group
	RequestID string
	Turn      int64
}

// ContinuationRequest binds immutable client-visible history to one session lifetime.
type ContinuationRequest struct {
	Session    Session
	Activation [32]byte
	ResponseID string
	History    json.RawMessage
}

// Continuation restores Responses delta input without trusting its derived thread key.
type Continuation struct {
	Scope   [32]byte
	History json.RawMessage
}

// Store owns short transactions; no lock spans a judge invocation.
type Store interface {
	Start(context.Context, StartRequest) (Session, error)
	Complete(context.Context, CompleteRequest) (Completion, error)
	FinishJob(context.Context, Job, Judgment, FailureCode) error
	Apply(context.Context, ApplyRequest) (bool, error)
	RecordNoTarget(context.Context, ApplyRequest) error
	SaveContinuation(context.Context, ContinuationRequest) error
	Continuation(context.Context, [32]byte, string) (Continuation, bool, error)
	ListJobs(context.Context, string, int) ([]Job, error)
	Summary(context.Context, string) (Summary, error)
	GetJob(context.Context, string, string) (Job, bool, error)
	ListSessions(context.Context, string, int32, int32) ([]Session, error)
	GetSession(context.Context, string, [32]byte) (Session, []Job, bool, error)
	SweepExpired(context.Context) error
}
