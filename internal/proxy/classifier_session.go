package proxy

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"

	"weave-os/router/internal/observability"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/router"
)

// ClassifierThreadHeader is a router-only token; clients must not forward a
// parent's token to a subagent or create a new token when compacting a thread.
const ClassifierThreadHeader = "X-Weave-Classifier-Thread"

// ClassifierThreadUnavailableToken lets a client fail closed when its hook
// runtime swallows an enrollment error. It is never a valid signed ticket.
const ClassifierThreadUnavailableToken = "weave-classifier-unavailable"
const classifierThreadIssuer = "weave-classifier-thread-v1"
const classifierThreadLifetime = 30 * 24 * time.Hour

var classifierReleasePattern = regexp.MustCompile(`^llm-classifier-v[0-9]+\.[0-9]+\.[0-9]+$`)
var classifierDigestPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)

// ClassifierSessionConfig is server-owned enrollment configuration. The release
// hash binds checkpoint, tokenizer and prompt; callers cannot choose either pin.
type ClassifierSessionConfig struct {
	Release               string
	ReleaseSHA256         string
	SelectionPolicySHA256 string
	SigningKey            []byte
	InstallationIDs       []uuid.UUID
}

type classifierSessions struct {
	config               ClassifierSessionConfig
	allowedInstallations map[uuid.UUID]struct{}
	store                router.ClassifierSessionStore
	classifier           router.AtomicClassifier
}

type classifierThreadClaims struct {
	jwt.RegisteredClaims
	Thread router.ClassifierThread `json:"thread"`
}
type classifierThreadContextKey struct{}

// WithClassifierSessions enables explicit new-chat enrollment, never a default
// strategy or header override. Dependencies must describe one immutable release.
func (s *Service) WithClassifierSessions(config ClassifierSessionConfig, store router.ClassifierSessionStore, classifier router.AtomicClassifier) error {
	if !classifierReleasePattern.MatchString(config.Release) || !classifierDigestPattern.MatchString(config.ReleaseSHA256) || !classifierDigestPattern.MatchString(config.SelectionPolicySHA256) || len(config.SigningKey) < 32 || len(config.InstallationIDs) == 0 || store == nil || classifier == nil {
		return errors.New("classifier sessions require release pins, a signing key, an installation allowlist, persistence and inference")
	}
	allowedInstallations := make(map[uuid.UUID]struct{}, len(config.InstallationIDs))
	for _, id := range config.InstallationIDs {
		if id == uuid.Nil {
			return errors.New("classifier installation must be nonzero")
		}
		allowedInstallations[id] = struct{}{}
	}
	config.SigningKey = append([]byte(nil), config.SigningKey...)
	s.classifierSessions = &classifierSessions{config: config, allowedInstallations: allowedInstallations, store: store, classifier: classifier}
	return nil
}

func (s *Service) classifierPrincipal(ctx context.Context) (router.ClassifierThread, error) {
	if s.classifierSessions == nil {
		return router.ClassifierThread{}, router.ErrClassifierUnavailable
	}
	// Managed serving needs its own attested Modal binding. A legacy side path
	// must not override a gateway-admitted release or subscriber profile.
	if _, managed := requestcontext.ServingIdentityFromContext(ctx); managed {
		return router.ClassifierThread{}, router.ErrClassifierUnavailable
	}
	installation := installationIDFromContext(ctx)
	credential, _ := ctx.Value(APIKeyIDContextKey{}).(string)
	if _, allowed := s.classifierSessions.allowedInstallations[installation]; !allowed || credential == "" {
		return router.ClassifierThread{}, router.ErrClassifierThreadInvalid
	}
	return router.ClassifierThread{InstallationID: installation, CredentialSHA256: sha256.Sum256([]byte(credential)), Release: s.classifierSessions.config.Release, ReleaseSHA256: s.classifierSessions.config.ReleaseSHA256, SelectionPolicySHA256: s.classifierSessions.config.SelectionPolicySHA256}, nil
}

// StartClassifierThread must be called only on an explicit new-chat/subagent
// event. Retries reuse requestID; normal requests and compactions never call it.
func (s *Service) StartClassifierThread(ctx context.Context, requestID uuid.UUID) (string, error) {
	thread, err := s.classifierPrincipal(ctx)
	if err != nil {
		return "", err
	}
	if requestID == uuid.Nil {
		return "", router.ErrClassifierThreadInvalid
	}
	thread.RequestID, thread.ThreadID = requestID, uuid.New()
	thread.ExpiresAt = s.clockNow().Add(classifierThreadLifetime).Truncate(time.Second)
	thread, err = s.classifierSessions.store.Create(ctx, thread)
	if err != nil {
		return "", fmt.Errorf("persist classifier handshake: %w", err)
	}
	if thread.Release != s.classifierSessions.config.Release || thread.ReleaseSHA256 != s.classifierSessions.config.ReleaseSHA256 || thread.SelectionPolicySHA256 != s.classifierSessions.config.SelectionPolicySHA256 || !thread.ExpiresAt.After(s.clockNow()) {
		return "", router.ErrClassifierThreadInvalid
	}
	claims := classifierThreadClaims{RegisteredClaims: jwt.RegisteredClaims{Issuer: classifierThreadIssuer, ExpiresAt: jwt.NewNumericDate(thread.ExpiresAt)}, Thread: thread}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(s.classifierSessions.config.SigningKey)
}

// AdmitClassifierThread validates both the signed thread and current server-side
// enrollment. An absent token preserves the caller's existing routing strategy.
func (s *Service) AdmitClassifierThread(ctx context.Context, threadToken string) (context.Context, error) {
	if threadToken == "" {
		return ctx, nil
	}
	principal, err := s.classifierPrincipal(ctx)
	if err != nil {
		return ctx, err
	}
	if len(threadToken) > 4096 {
		return ctx, router.ErrClassifierThreadInvalid
	}
	claims := &classifierThreadClaims{}
	_, err = jwt.ParseWithClaims(threadToken, claims, func(*jwt.Token) (any, error) { return s.classifierSessions.config.SigningKey, nil }, jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}), jwt.WithIssuer(classifierThreadIssuer), jwt.WithExpirationRequired(), jwt.WithTimeFunc(s.clockNow))
	thread := claims.Thread
	if err != nil || thread.InstallationID != principal.InstallationID || thread.CredentialSHA256 != principal.CredentialSHA256 || thread.Release != principal.Release || thread.ReleaseSHA256 != principal.ReleaseSHA256 || thread.SelectionPolicySHA256 != principal.SelectionPolicySHA256 || thread.ThreadID == uuid.Nil || !thread.ExpiresAt.After(s.clockNow()) {
		return ctx, router.ErrClassifierThreadInvalid
	}
	ctx = context.WithValue(ctx, classifierThreadContextKey{}, thread)
	return router.WithStrategy(ctx, router.StrategyLLMClassifier), nil
}

func (s *Service) classifyThread(ctx context.Context, input router.ClassifierContext) (router.ClassifierPrediction, error) {
	thread, ok := ctx.Value(classifierThreadContextKey{}).(router.ClassifierThread)
	if !ok || s.classifierSessions == nil {
		return router.ClassifierPrediction{}, router.ErrClassifierThreadInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	var prediction router.ClassifierPrediction
	err := s.classifierSessions.store.WithThread(ctx, thread, func(turns router.ClassifierTurnStore) error {
		rootDigest, err := turns.RootTurnDigest(ctx)
		if err != nil {
			return err
		}
		if rootDigest != "" && rootDigest != input.RootTurnDigest {
			return router.ErrClassifierHistoryUnavailable
		}
		stored, found, err := turns.Get(ctx, input.TurnDigest)
		if err != nil {
			return err
		}
		if found {
			if stored.RootTurnDigest != input.RootTurnDigest || stored.Features != input.Features || stored.CompletedResponseCount != input.CompletedResponseCount {
				return router.ErrClassifierHistoryUnavailable
			}
			prediction = stored
			return nil
		}
		if !input.AtUserBoundary {
			return router.ErrClassifierHistoryUnavailable
		}
		if input.Features.UserMessageCount == 1 {
			if input.PreviousTurnDigest != "" || input.RootTurnDigest != input.TurnDigest || input.Features.ToolCallCount != 0 {
				return router.ErrClassifierHistoryUnavailable
			}
		} else {
			previous, found, err := turns.Get(ctx, input.PreviousTurnDigest)
			if err != nil {
				return err
			}
			if !found || previous.RootTurnDigest != input.RootTurnDigest || previous.Features.UserMessageCount+1 != input.Features.UserMessageCount || previous.Features.ToolCallCount > input.Features.ToolCallCount || previous.Features.ToolErrorCount > input.Features.ToolErrorCount || previous.CompletedResponseCount > input.CompletedResponseCount {
				return router.ErrClassifierHistoryUnavailable
			}
		}
		history := make(map[string]router.ClassifierComplexity)
		for _, response := range input.PrecedingResponses {
			if _, found := history[response.TurnDigest]; found {
				continue
			}
			previous, found, err := turns.Get(ctx, response.TurnDigest)
			if err != nil {
				return err
			}
			if !found || previous.RootTurnDigest != input.RootTurnDigest {
				return router.ErrClassifierHistoryUnavailable
			}
			history[response.TurnDigest] = previous.Complexity
		}
		request, err := input.WithHistoricalPredictions(history)
		if err != nil {
			return err
		}
		prediction, err = s.classifierSessions.classifier.Classify(ctx, request)
		if err != nil {
			return err
		}
		prediction.TurnDigest, prediction.RootTurnDigest = input.TurnDigest, input.RootTurnDigest
		prediction.Features, prediction.CompletedResponseCount = input.Features, input.CompletedResponseCount
		if err := prediction.Validate(); err != nil {
			return err
		}
		return turns.Insert(ctx, prediction)
	})
	if err != nil {
		observability.FromContext(ctx).Warn("Classifier thread classification failed", "err", err, "classifier_release", thread.Release, "thread_id", thread.ThreadID, "installation_id", thread.InstallationID)
		if errors.Is(err, router.ErrClassifierHistoryUnavailable) || errors.Is(err, router.ErrClassifierThreadInvalid) || errors.Is(err, router.ErrClassifierInputTooLong) {
			return router.ClassifierPrediction{}, err
		}
		return router.ClassifierPrediction{}, fmt.Errorf("classify thread: %w: %w", err, router.ErrClassifierUnavailable)
	}
	return prediction, nil
}
