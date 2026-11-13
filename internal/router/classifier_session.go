package router

import (
	"context"
	"errors"
	"math"
	"time"

	"github.com/google/uuid"
)

// ErrClassifierThreadInvalid rejects a missing, foreign or retired thread binding.
var ErrClassifierThreadInvalid = errors.New("classifier thread is invalid or unavailable")

// ErrClassifierUnavailable never authorizes substitution with another classifier.
var ErrClassifierUnavailable = errors.New("llm classifier unavailable")

// ErrClassifierInputTooLong rejects overflow without truncating the V3 input.
var ErrClassifierInputTooLong = errors.New("classifier input exceeds inference limit")

// ClassifierThread is a durable, authenticated new-chat handshake. RequestID makes
// handshake retries idempotent; ThreadID is minted by the server, not a parent ID.
type ClassifierThread struct {
	InstallationID        uuid.UUID
	CredentialSHA256      [32]byte
	RequestID             uuid.UUID
	ThreadID              uuid.UUID
	Release               string
	ReleaseSHA256         string
	SelectionPolicySHA256 string
	ExpiresAt             time.Time
}

// ClassifierPrediction stores classification facts only, never prompt content.
type ClassifierPrediction struct {
	TurnDigest             string
	RootTurnDigest         string
	InputMessageCount      int
	Features               ClassifierFeatures
	CompletedResponseCount int
	Complexity             ClassifierComplexity
	Probabilities          []float64
}

// Validate checks facts crossing the service or persistence boundary.
func (p ClassifierPrediction) Validate() error {
	if p.Complexity < ClassifierLow || p.Complexity > ClassifierMaximum || len(p.Probabilities) != 4 {
		return ErrClassifierUnavailable
	}
	total, winner := 0.0, 0
	for index, probability := range p.Probabilities {
		if math.IsNaN(probability) || math.IsInf(probability, 0) || probability < 0 || probability > 1 {
			return ErrClassifierUnavailable
		}
		total += probability
		if probability > p.Probabilities[winner] {
			winner = index
		}
	}
	if math.Abs(total-1) > 1e-6 || ClassifierComplexity(winner) != p.Complexity {
		return ErrClassifierUnavailable
	}
	return nil
}

// ClassifierPrefixCheckpoint fences rewrites of previously admitted tool loops
// and instruction updates without persisting any conversation content.
type ClassifierPrefixCheckpoint struct {
	MessageCount int
	Digest       string
}

// ClassifierTurnStore is scoped to one locked thread and immutable release.
type ClassifierTurnStore interface {
	PrefixCheckpoint() ClassifierPrefixCheckpoint
	SetPrefixCheckpoint(context.Context, ClassifierPrefixCheckpoint) error
	Get(context.Context, string) (ClassifierPrediction, bool, error)
	PredictionBeforeMessage(context.Context, int) (ClassifierPrediction, bool, error)
	RootTurnDigest(context.Context) (string, error)
	Insert(context.Context, ClassifierPrediction) error
}

// ClassifierSessionStore serializes inference and persistence per thread. A
// successful callback commits before provider dispatch; an error rolls back.
type ClassifierSessionStore interface {
	Create(context.Context, ClassifierThread) (ClassifierThread, error)
	WithThread(context.Context, ClassifierThread, func(ClassifierTurnStore) error) error
}

// AtomicClassifier receives the exact V3 prompt contract, not clipped policy text.
type AtomicClassifier interface {
	Classify(context.Context, AtomicClassificationRequest) (ClassifierPrediction, error)
}
