package taskdomain

import (
	"context"
	"errors"
	"regexp"
	"time"
)

type Domain string

const (
	UI                    Domain = "ui"
	Logic                 Domain = "logic"
	Data                  Domain = "data"
	Infra                 Domain = "infra"
	Docs                  Domain = "docs"
	AuxiliaryModel               = "task_domain"
	SchemaVersion                = "task_domain_classifier_v1"
	ProjectionVersion            = "initial_logical_user_turn_v1"
	MaxInputBytes                = 32_768
	MaxInputTokens               = 8_192
	Timeout                      = 3 * time.Second
	BudgetHeader                 = "X-Task-Domain-Budget-Ms"
	MaxBudgetMilliseconds        = 10_000
)

type Profile map[Domain]bool

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

type Status string

const (
	Ready               Status = "ready"
	Unavailable         Status = "unavailable"
	TimedOut            Status = "timeout"
	NoTask              Status = "no_task"
	EvidenceUnavailable Status = "evidence_unavailable"
)

type Outcome struct {
	Status         Status  `json:"status"`
	Profile        Profile `json:"profile,omitempty"`
	ReleaseSHA256  string  `json:"release_sha256"`
	EvidenceSHA256 string  `json:"evidence_sha256,omitempty"`
	Cached         bool    `json:"cached,omitempty"`
}

type Input struct {
	ConversationKey string
	RootSHA256      string
	UserText        string
	Resume          bool
}

type Key struct {
	Conversation string
	Root         string
	Release      string
	Evidence     string
}

type Store interface {
	Resolve(context.Context, Key, bool, func(context.Context) Outcome) (Outcome, error)
}

type Classifier interface {
	Classify(context.Context, string) (Profile, error)
}

type Resolver interface {
	Resolve(context.Context, Input) Outcome
}

var digestPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)

func ValidDigest(value string) bool { return digestPattern.MatchString(value) }
