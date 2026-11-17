package router

import (
	"context"
	"errors"
	"math"
	"time"

	"github.com/google/uuid"
)

var ErrClassifierThreadInvalid = errors.New("classifier thread is invalid or unavailable")

var ErrClassifierUnavailable = errors.New("llm classifier unavailable")

var ErrClassifierInputTooLong = errors.New("classifier input exceeds inference limit")

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

type ClassifierPrediction struct {
	TurnDigest             string
	RootTurnDigest         string
	InputMessageCount      int
	Features               ClassifierFeatures
	CompletedResponseCount int
	Complexity             ClassifierComplexity
	Probabilities          []float64
}

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

type ClassifierPrefixCheckpoint struct {
	MessageCount int
	Digest       string
}

type ClassifierTurnStore interface {
	PrefixCheckpoint() ClassifierPrefixCheckpoint
	SetPrefixCheckpoint(context.Context, ClassifierPrefixCheckpoint) error
	Get(context.Context, string) (ClassifierPrediction, bool, error)
	PredictionBeforeMessage(context.Context, int) (ClassifierPrediction, bool, error)
	RootTurnDigest(context.Context) (string, error)
	Insert(context.Context, ClassifierPrediction) error
}

type ClassifierSessionStore interface {
	Create(context.Context, ClassifierThread) (ClassifierThread, error)
	WithThread(context.Context, ClassifierThread, func(ClassifierTurnStore) error) error
}

type AtomicClassifier interface {
	Classify(context.Context, AtomicClassificationRequest) (ClassifierPrediction, error)
}
