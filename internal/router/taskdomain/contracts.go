// Package taskdomain defines optional, conversation-scoped task classification.
package taskdomain

import (
	"context"
	"errors"
	"regexp"
	"time"
)

// Domain is independent of the HMM complexity taxonomy.
type Domain string

const (
	UI                Domain = "ui"
	Logic             Domain = "logic"
	Data              Domain = "data"
	Infra             Domain = "infra"
	Docs              Domain = "docs"
	AuxiliaryModel           = "task_domain"
	SchemaVersion            = "task_domain_classifier_v1"
	ProjectionVersion        = "initial_logical_user_turn_v1"
	MaxInputBytes            = 32_768
	MaxInputTokens           = 8_192
	Timeout                  = 3 * time.Second
	// BudgetHeader carries the caller's remaining milliseconds so the service can drop work it would finish too late.
	BudgetHeader = "X-Task-Domain-Budget-Ms"
	// MaxBudgetMilliseconds is the largest budget the service accepts.
	MaxBudgetMilliseconds = 10_000
)

// Profile distinguishes a valid all-zero prediction from absent evidence.
type Profile map[Domain]bool

// Validate requires every independent bit, including false values.
func (p Profile) Validate() error {
	if len(p) != 5 {
		return errors.New("task profile requires five bits")
	}
	for _, domain := range []Domain{UI, Logic, Data, Infra, Docs} {
		if _, exists := p[domain]; !exists {
			return errors.New("task profile has an unknown or missing domain")
		}
	}
	return nil
}

// Status explains why an optional profile was or was not available.
type Status string

const (
	Ready               Status = "ready"
	Unavailable         Status = "unavailable"
	TimedOut            Status = "timeout"
	NoTask              Status = "no_task"
	EvidenceUnavailable Status = "evidence_unavailable"
)

// Outcome is content-free and safe to persist and include in selection traces.
type Outcome struct {
	Status         Status  `json:"status"`
	Profile        Profile `json:"profile,omitempty"`
	ReleaseSHA256  string  `json:"release_sha256"`
	EvidenceSHA256 string  `json:"evidence_sha256,omitempty"`
	Cached         bool    `json:"cached,omitempty"`
}

// Input is constructed only by authenticated proxy orchestration, never decoded from a header.
type Input struct {
	ConversationKey string
	RootSHA256      string
	UserText        string
	Resume          bool
}

// Key isolates task state across tenants, logical roots and immutable releases.
type Key struct {
	Conversation string
	Root         string
	Release      string
	Evidence     string
}

// Store serializes first classification across replicas and persists no prompt text.
type Store interface {
	Resolve(context.Context, Key, bool, func(context.Context) Outcome) (Outcome, error)
}

// Classifier returns five bits from one pinned task-domain model.
type Classifier interface {
	Classify(context.Context, string) (Profile, error)
}

// Resolver is the bounded orchestration seam used by the complexity client.
type Resolver interface {
	Resolve(context.Context, Input) Outcome
}

var digestPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)

// ValidDigest validates identities at configuration and persistence boundaries.
func ValidDigest(value string) bool { return digestPattern.MatchString(value) }
