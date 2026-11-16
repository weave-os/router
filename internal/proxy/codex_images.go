package proxy

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/google/uuid"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/credentials"
	"weave-os/router/internal/observability"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/subscriptions"
	"weave-os/router/internal/subscriptions/entitlement"

	"github.com/tidwall/gjson"
)

const (
	CodexImageGenerations = "generations"
	CodexImageEdits       = "edits"
)

var (
	ErrCodexImagesRequireOwnCredential   = errors.New("image generation through the router requires signing in to Codex with ChatGPT, an OpenAI API key, or an OpenAI key saved in Weave")
	ErrCodexImagesUnavailableWithGateway = errors.New("image generation through the router is unavailable while a gateway key serves this installation")
)

type codexImageForwarder interface {
	ForwardCodexImageRequest(context.Context, string, []byte, http.Header, http.ResponseWriter) error
}

type CodexImages struct {
	svc *Service
}

func NewCodexImages(svc *Service) CodexImages {
	return CodexImages{svc: svc}
}

func (c CodexImages) ForwardWithCallerCredential(ctx context.Context, headers http.Header, operation string, body []byte, w http.ResponseWriter) error {
	if operation != CodexImageGenerations && operation != CodexImageEdits {
		return fmt.Errorf("unsupported Codex image operation %q", operation)
	}
	client, err := c.svc.provider(providers.ProviderOpenAI)
	if err != nil {
		return err
	}
	forwarder, ok := client.(codexImageForwarder)
	if !ok {
		return fmt.Errorf("OpenAI provider does not support Codex image requests")
	}
	if len(credentials.GatewayProviders(externalKeysFromContext(ctx))) > 0 {
		return ErrCodexImagesUnavailableWithGateway
	}
	if creds := codexImageCallerCredential(ctx, headers); creds != nil {
		return forwarder.ForwardCodexImageRequest(requestcontext.WithCredentials(ctx, creds), operation, body, headers, w)
	}
	served, err := c.forwardOnLinkedSubscription(ctx, forwarder, operation, body, headers, w)
	var unsent *providers.UpstreamErrorResponse
	if served && !errors.As(err, &unsent) {
		return err
	}
	if byok := codexImageBYOK(ctx); byok != nil {
		return forwarder.ForwardCodexImageRequest(requestcontext.WithCredentials(ctx, byok), operation, body, headers, w)
	}
	if served {
		return err
	}
	return ErrCodexImagesRequireOwnCredential
}

func codexImageCallerCredential(ctx context.Context, headers http.Header) *Credentials {
	if byokFirst(ctx, providers.ProviderOpenAI) {
		if byok := codexImageBYOK(ctx); byok != nil {
			return byok
		}
	}
	return ExtractClientCredentials(providers.ProviderOpenAI, headers)
}

func codexImageBYOK(ctx context.Context) *Credentials {
	if byok := usableBYOK(ctx, providers.ProviderOpenAI); byok != nil && byok.BaseURL == "" {
		return byok
	}
	return nil
}

func (c CodexImages) forwardOnLinkedSubscription(ctx context.Context, forwarder codexImageForwarder, operation string, body []byte, headers http.Header, w http.ResponseWriter) (bool, error) {
	svc := c.svc
	if svc.managedSubscriptions == nil || !managedSubscriptionEnrolled(ctx, subscriptions.ProviderCodex) {
		return false, nil
	}
	owner := subscriptionOwnerFromContext(ctx)
	lease, present, err := svc.managedSubscriptions.Lease(ctx, owner, subscriptions.ProviderCodex, ClientIdentityFrom(ctx).SessionID)
	if err != nil && !errors.Is(err, subscriptions.ErrNoAvailableAccount) {
		return true, errors.Join(ErrSubscriptionPoolUnavailable, err)
	}
	if err != nil || !present {
		return false, nil
	}
	defer lease.Release()
	creds := &Credentials{
		APIKey:                []byte(lease.AccessToken),
		OAuth:                 true,
		Source:                credSourceCodexSubscription,
		AccountID:             []byte(lease.ProviderAccount),
		PrincipalID:           "chatgpt-account:" + lease.ProviderAccount,
		SubscriptionAccountID: lease.AccountID,
	}
	credentialCtx := requestcontext.WithCredentials(ctx, creds)
	started := svc.clockNow()
	attemptErr := forwarder.ForwardCodexImageRequest(credentialCtx, operation, body, headers, w)
	bookkeepingCtx := context.WithoutCancel(credentialCtx)
	recordCodexImageLeaseOutcome(bookkeepingCtx, svc, owner, lease, attemptErr)
	if attemptErr == nil {
		svc.markManagedSubscriptionServed(ctx, credentialCtx, lease)
	}
	recordCodexImageUsage(bookkeepingCtx, svc, gjson.GetBytes(body, "model").String(), started, attemptErr)
	return true, attemptErr
}

func recordCodexImageLeaseOutcome(ctx context.Context, svc *Service, owner auth.SubscriptionOwner, lease subscriptions.Lease, attemptErr error) {
	var relayed *providers.UpstreamStatusError
	if errors.As(attemptErr, &relayed) {
		attemptErr = &providers.UpstreamErrorResponse{Status: relayed.Status, Headers: relayed.Headers, Body: relayed.Body, BodyBytes: relayed.BodyBytes}
	}
	pool := subscriptions.ProviderCodex
	log := observability.FromContext(ctx).With("provider", pool, "account_id", lease.AccountID)
	switch upstreamStatus(attemptErr) {
	case http.StatusOK:
		health, ok := svc.managedSubscriptions.(interface {
			Activate(context.Context, auth.SubscriptionOwner, subscriptions.Provider, string) error
		})
		if attemptErr != nil || lease.State == auth.SubscriptionAccountStateActive || !ok {
			return
		}
		if err := health.Activate(ctx, owner, pool, lease.AccountID); err != nil {
			log.Error("Failed to mark subscription account active", "err", err)
		}
	case http.StatusTooManyRequests:
		if resetAt, exhausted := svc.subscriptionQuotaExhaustion(pool, lease, attemptErr); exhausted {
			recordManagedQuotaProof(ctx, lease.AccountID, resetAt)
			if err := exhaustManagedSubscription(ctx, svc.managedSubscriptions, owner, pool, lease.AccountID, resetAt); err != nil {
				log.Error("Failed to persist exhausted subscription account", "err", err)
			}
			return
		}
		resetAt := managedSubscriptionResetAt(attemptErr, svc.clockNow())
		if err := svc.managedSubscriptions.Cooldown(ctx, owner, pool, lease.AccountID, resetAt); err != nil {
			log.Error("Failed to persist subscription account cooldown", "err", err)
		}
		recordManagedThrottle(ctx, lease.AccountID, resetAt)
	case http.StatusUnauthorized:
		if _, err := rejectManagedSubscriptionCredential(ctx, svc.managedSubscriptions, owner, pool, lease.AccountID, http.StatusUnauthorized); err != nil {
			log.Error("Failed to record rejected subscription account", "err", err)
		}
	}
}

func recordCodexImageUsage(ctx context.Context, svc *Service, model string, started time.Time, attemptErr error) {
	installationID := installationIDFromContext(ctx)
	if installationID == uuid.Nil {
		return
	}
	requestID := observability.RequestIDFromContext(ctx)
	latency := svc.clockNow().Sub(started).Milliseconds()
	params := InsertTelemetryParams{
		InstallationID:     installationID.String(),
		APIKeyID:           apiKeyIDFromContext(ctx),
		RequestID:          requestID,
		SpanType:           "router.upstream",
		TraceID:            requestID,
		Timestamp:          started,
		RequestedModel:     model,
		DecisionModel:      model,
		DecisionProvider:   providers.ProviderOpenAI,
		UpstreamLatencyMs:  latency,
		TotalLatencyMs:     latency,
		UpstreamStatusCode: int32(upstreamStatus(attemptErr)),
		CredentialSource:   credSourceCodexSubscription,
		CapacitySource:     string(entitlement.CapacitySourceLinkedCodex),
	}
	applySubscriptionWinnerTelemetry(ctx, &params)
	svc.fireTelemetry(ctx, params)
}
