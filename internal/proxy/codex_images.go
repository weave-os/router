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
	"weave-os/router/internal/ingress"
	"weave-os/router/internal/observability"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/subscriptions"
	"weave-os/router/internal/subscriptions/entitlement"
	"weave-os/router/internal/telemetry/turnrecord"

	"github.com/tidwall/gjson"
)

const (
	CodexImageGenerations = "generations"
	CodexImageEdits       = "edits"
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
	if len(credentials.GatewayProviders(credentials.ExternalKeysFromContext(ctx))) > 0 {
		return ingress.ErrCodexImagesUnavailableWithGateway
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
	return ingress.ErrCodexImagesRequireOwnCredential
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
	if svc.credentials.ManagedSubscriptions == nil || !managedSubscriptionEnrolled(ctx, subscriptions.ProviderCodex) {
		return false, nil
	}
	owner := subscriptionOwnerFromContext(ctx)
	lease, present, err := svc.credentials.ManagedSubscriptions.Lease(ctx, owner, subscriptions.ProviderCodex, ClientIdentityFrom(ctx).SessionID)
	if err != nil && !errors.Is(err, subscriptions.ErrNoAvailableAccount) {
		return true, errors.Join(credentials.ErrSubscriptionPoolUnavailable, err)
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
	started := svc.clock.Now()
	attemptErr := forwarder.ForwardCodexImageRequest(credentialCtx, operation, body, headers, w)
	bookkeepingCtx := context.WithoutCancel(credentialCtx)
	recordCodexImageLeaseOutcome(bookkeepingCtx, svc, owner, lease, attemptErr)
	if attemptErr == nil {
		svc.markManagedSubscriptionServed(ctx, credentialCtx, lease)
	}
	var unsent *providers.UpstreamErrorResponse
	if !errors.As(attemptErr, &unsent) {
		recordCodexImageUsage(bookkeepingCtx, svc, gjson.GetBytes(body, "model").String(), started, attemptErr)
	}
	return true, attemptErr
}

func recordCodexImageLeaseOutcome(ctx context.Context, svc *Service, owner auth.SubscriptionOwner, lease subscriptions.Lease, attemptErr error) {
	if attemptErr == nil {
		activateManagedSubscription(ctx, svc.credentials.ManagedSubscriptions, owner, subscriptions.ProviderCodex, lease)
		return
	}
	var relayed *providers.UpstreamStatusError
	if errors.As(attemptErr, &relayed) {
		attemptErr = &providers.UpstreamErrorResponse{Status: relayed.Status, Headers: relayed.Headers, Body: relayed.Body, BodyBytes: relayed.BodyBytes}
	}
	if !codexImagePlanLacksImageAccess(attemptErr) {
		recordManagedSubscriptionStatus(ctx, svc, owner, subscriptions.ProviderCodex, lease, attemptErr)
	}
}

func codexImagePlanLacksImageAccess(attemptErr error) bool {
	return upstreamStatus(attemptErr) == http.StatusForbidden
}

func recordCodexImageUsage(ctx context.Context, svc *Service, model string, started time.Time, attemptErr error) {
	installationID := installationIDFromContext(ctx)
	if installationID == uuid.Nil {
		return
	}
	requestID := observability.RequestIDFromContext(ctx)
	latency := svc.clock.Now().Sub(started).Milliseconds()
	params := turnrecord.InsertTelemetryParams{
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
