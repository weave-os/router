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

// ClassifierThreadHeader is a router-only token. General subagents enroll
// independently; the native one-shot search helper is isolated server-side.
// Compaction must never create a new token.
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

func (s *Service) withClassifierSearchChild(ctx context.Context, input router.ClassifierContext) (context.Context, error) {
	parent, ok := ctx.Value(classifierThreadContextKey{}).(router.ClassifierThread)
	if !ok || s.classifierSessions == nil {
		observability.FromContext(ctx).Warn("Classifier search child has no admitted parent")
		return ctx, router.ErrClassifierThreadInvalid
	}
	log := observability.FromContext(ctx).With("parent_thread_id", parent.ThreadID, "installation_id", parent.InstallationID)
	// A native helper inherits the process-wide header, but none of the parent's
	// history. Its exact root under this parent is a retry-stable one-shot identity.
	storeCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	err := s.classifierSessions.store.WithThread(storeCtx, parent, func(router.ClassifierTurnStore) error { return nil })
	if err != nil {
		log.Error("Failed to validate classifier search parent", "err", err)
		return ctx, fmt.Errorf("validate search parent: %w: %w", err, router.ErrClassifierUnavailable)
	}
	child := parent
	child.RequestID = uuid.NewSHA1(parent.ThreadID, []byte("native-search-child-v1:"+input.RootTurnDigest))
	child.ThreadID = uuid.New()
	child, err = s.classifierSessions.store.Create(storeCtx, child)
	if err != nil {
		log.Error("Failed to persist classifier search child", "err", err)
		return ctx, fmt.Errorf("persist search child: %w: %w", err, router.ErrClassifierUnavailable)
	}
	if child.Release != parent.Release || child.ReleaseSHA256 != parent.ReleaseSHA256 || child.SelectionPolicySHA256 != parent.SelectionPolicySHA256 || child.ExpiresAt.After(parent.ExpiresAt) || !child.ExpiresAt.After(s.clockNow()) {
		log.Warn("Classifier search child metadata rejected", "child_thread_id", child.ThreadID, "err", router.ErrClassifierThreadInvalid)
		return ctx, router.ErrClassifierThreadInvalid
	}
	return context.WithValue(ctx, classifierThreadContextKey{}, child), nil
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
		checkpoint := turns.PrefixCheckpoint()
		if len(input.PrefixDigests) == 0 || checkpoint.MessageCount > len(input.PrefixDigests) ||
			(checkpoint.MessageCount > 0 && input.PrefixDigests[checkpoint.MessageCount-1] != checkpoint.Digest) {
			observability.FromContext(ctx).Warn("Classifier request prefix checkpoint rejected", "thread_id", thread.ThreadID, "checkpoint_message_count", checkpoint.MessageCount, "input_prefix_count", len(input.PrefixDigests))
			return router.ErrClassifierHistoryUnavailable
		}
		rootDigest, err := turns.RootTurnDigest(ctx)
		if err != nil {
			return err
		}
		// An old thread without a checkpoint cannot attest its tool-loop prefix.
		if rootDigest != "" && checkpoint.MessageCount == 0 {
			observability.FromContext(ctx).Warn("Classifier thread has predictions but no request checkpoint", "thread_id", thread.ThreadID)
			return router.ErrClassifierHistoryUnavailable
		}
		err = turns.SetPrefixCheckpoint(ctx, router.ClassifierPrefixCheckpoint{MessageCount: len(input.PrefixDigests), Digest: input.PrefixDigests[len(input.PrefixDigests)-1]})
		if err != nil {
			observability.FromContext(ctx).Error("Failed to persist classifier request checkpoint", "thread_id", thread.ThreadID, "message_count", len(input.PrefixDigests), "err", err)
			return err
		}
		stored, found, err := turns.Get(ctx, input.TurnDigest)
		if err != nil {
			return err
		}
		if found {
			if stored.InputMessageCount != len(input.PrefixDigests) || stored.RootTurnDigest != rootDigest || stored.Features != input.Features || stored.CompletedResponseCount != input.CompletedResponseCount {
				observability.FromContext(ctx).Warn("Classifier stored prediction provenance mismatch", "thread_id", thread.ThreadID, "stored_input_message_count", stored.InputMessageCount, "input_message_count", len(input.PrefixDigests), "root_matches", stored.RootTurnDigest == rootDigest, "features_match", stored.Features == input.Features, "stored_completed_response_count", stored.CompletedResponseCount, "completed_response_count", input.CompletedResponseCount)
				return router.ErrClassifierHistoryUnavailable
			}
			prediction = stored
			return nil
		}
		if checkpoint.MessageCount == 0 {
			if rootDigest != "" || input.HasAssistantHistory || input.Features.ToolCallCount != 0 || input.CompletedResponseCount != 0 {
				observability.FromContext(ctx).Warn("Classifier initial request state rejected", "thread_id", thread.ThreadID, "has_root_digest", rootDigest != "", "has_assistant_history", input.HasAssistantHistory, "tool_call_count", input.Features.ToolCallCount, "completed_response_count", input.CompletedResponseCount)
				return router.ErrClassifierHistoryUnavailable
			}
			rootDigest = input.TurnDigest
		} else {
			previous, found, err := turns.Get(ctx, checkpoint.Digest)
			if err != nil {
				return err
			}
			if !found || previous.InputMessageCount != checkpoint.MessageCount || previous.RootTurnDigest != rootDigest || previous.Features.UserMessageCount > input.Features.UserMessageCount || previous.Features.ToolCallCount > input.Features.ToolCallCount || previous.Features.ToolErrorCount > input.Features.ToolErrorCount || previous.CompletedResponseCount > input.CompletedResponseCount {
				observability.FromContext(ctx).Warn("Classifier checkpoint consistency rejected", "thread_id", thread.ThreadID, "checkpoint_message_count", checkpoint.MessageCount, "previous_found", found, "previous_input_message_count", previous.InputMessageCount, "root_matches", previous.RootTurnDigest == rootDigest, "previous_user_message_count", previous.Features.UserMessageCount, "user_message_count", input.Features.UserMessageCount, "previous_tool_call_count", previous.Features.ToolCallCount, "tool_call_count", input.Features.ToolCallCount, "previous_tool_error_count", previous.Features.ToolErrorCount, "tool_error_count", input.Features.ToolErrorCount, "previous_completed_response_count", previous.CompletedResponseCount, "completed_response_count", input.CompletedResponseCount)
				return router.ErrClassifierHistoryUnavailable
			}
		}
		history := make(map[string]router.ClassifierComplexity)
		for _, response := range input.PrecedingResponses {
			if _, found := history[response.PrefixDigest]; found {
				continue
			}
			previous, found, err := turns.PredictionBeforeMessage(ctx, response.MessageIndex)
			if err != nil {
				return err
			}
			if !found || previous.RootTurnDigest != rootDigest || previous.InputMessageCount <= 0 || previous.InputMessageCount > response.MessageIndex || previous.Features.UserMessageCount != response.UserMessageCount || previous.TurnDigest != input.PrefixDigests[previous.InputMessageCount-1] {
				observability.FromContext(ctx).Warn("Classifier response ownership rejected", "thread_id", thread.ThreadID, "response_index", response.ResponseIndex, "message_index", response.MessageIndex, "response_user_message_count", response.UserMessageCount, "prediction_found", found, "prediction_input_message_count", previous.InputMessageCount, "prediction_user_message_count", previous.Features.UserMessageCount, "root_matches", previous.RootTurnDigest == rootDigest)
				return router.ErrClassifierHistoryUnavailable
			}
			history[response.PrefixDigest] = previous.Complexity
		}
		request, err := input.WithHistoricalPredictions(history)
		if err != nil {
			return err
		}
		prediction, err = s.classifierSessions.classifier.Classify(ctx, request)
		if err != nil {
			return err
		}
		prediction.TurnDigest, prediction.RootTurnDigest = input.TurnDigest, rootDigest
		prediction.InputMessageCount = len(input.PrefixDigests)
		prediction.Features, prediction.CompletedResponseCount = input.Features, input.CompletedResponseCount
		if err := prediction.Validate(); err != nil {
			return err
		}
		err = turns.Insert(ctx, prediction)
		if err == nil {
			observability.FromContext(ctx).Debug("Classifier API call classified", "thread_id", thread.ThreadID, "input_message_count", prediction.InputMessageCount, "completed_response_count", prediction.CompletedResponseCount, "tool_call_count", prediction.Features.ToolCallCount, "tool_error_count", prediction.Features.ToolErrorCount, "complexity", prediction.Complexity)
		}
		return err
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
