package admin

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/observability"
	"weave-os/router/internal/subscriptions"

	"github.com/gin-gonic/gin"
)

// SubscriptionLoginFlow runs the provider side of a browser login.
type SubscriptionLoginFlow interface {
	StartLogin(context.Context, subscriptions.Provider) (subscriptions.StartedLogin, error)
	CompleteLogin(context.Context, subscriptions.PendingLogin, string) (subscriptions.CompletedLogin, error)
}

type internalStartSubscriptionLoginRequest struct {
	Provider auth.SubscriptionProvider `json:"provider" binding:"required"`
}

type internalStartSubscriptionLoginResponse struct {
	LoginToken          string    `json:"login_token"`
	VerificationURL     string    `json:"verification_url"`
	UserCode            string    `json:"user_code,omitempty"`
	PollIntervalSeconds int       `json:"poll_interval_seconds,omitempty"`
	ExpiresAt           time.Time `json:"expires_at"`
}

type internalCompleteSubscriptionLoginRequest struct {
	LoginToken string `json:"login_token" binding:"required"`
	// Code is Claude's pasted `code#state`; Codex completes by polling without one.
	Code string `json:"code"`
}

// InternalStartSubscriptionLoginHandler begins a browser reconnect for a
// subscriber. The pending provider state is sealed into the returned token, so
// any replica can complete it.
func InternalStartSubscriptionLoginHandler(authSvc *auth.Service, flow SubscriptionLoginFlow) gin.HandlerFunc {
	return func(c *gin.Context) {
		owner, ok := internalSubscriptionOwner(c)
		if !ok {
			return
		}
		var request internalStartSubscriptionLoginRequest
		if err := c.ShouldBindJSON(&request); err != nil {
			c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "invalid_subscription_login"})
			return
		}
		log := observability.FromGin(c).With("provider", request.Provider, "subscriber_id", owner.SubscriberID)
		started, err := flow.StartLogin(c.Request.Context(), subscriptions.Provider(request.Provider))
		if err != nil {
			log.Warn("Failed to start subscription login", "err", err)
			c.AbortWithStatusJSON(http.StatusBadGateway, gin.H{"error": "subscription_login_unavailable"})
			return
		}
		token, err := sealSubscriptionLogin(authSvc, owner.SubscriberID, started.Pending)
		if err != nil {
			log.Error("Failed to seal subscription login", "err", err)
			c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "subscription_login_unavailable"})
			return
		}
		c.JSON(http.StatusOK, internalStartSubscriptionLoginResponse{
			LoginToken:          token,
			VerificationURL:     started.VerificationURL,
			UserCode:            started.UserCode,
			PollIntervalSeconds: int(started.PollInterval / time.Second),
			ExpiresAt:           started.Pending.ExpiresAt,
		})
	}
}

// InternalCompleteSubscriptionLoginHandler finishes a browser login and
// enrolls (or reconnects) the account through the same path as `router login`.
// A Codex login the user has not approved yet answers 202 so the caller polls.
func InternalCompleteSubscriptionLoginHandler(authSvc *auth.Service, flow SubscriptionLoginFlow) gin.HandlerFunc {
	return func(c *gin.Context) {
		owner, ok := internalSubscriptionOwner(c)
		if !ok {
			return
		}
		var request internalCompleteSubscriptionLoginRequest
		if err := c.ShouldBindJSON(&request); err != nil {
			c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "invalid_subscription_login"})
			return
		}
		log := observability.FromGin(c).With("subscriber_id", owner.SubscriberID)
		pending, err := openSubscriptionLogin(authSvc, owner.SubscriberID, request.LoginToken)
		if err != nil {
			log.Warn("Rejected subscription login token", "err", err)
			c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "invalid_subscription_login"})
			return
		}
		log = log.With("provider", pending.Provider)
		enrollmentOwner, err := authSvc.SubscriberEnrollmentOwner(c.Request.Context(), owner.SubscriberID)
		if errors.Is(err, auth.ErrSubscriptionEnrollmentKeyNotFound) {
			log.Warn("Subscription login has no routing key to enroll through")
			c.AbortWithStatusJSON(http.StatusConflict, gin.H{"error": "routing_key_required"})
			return
		}
		if err != nil {
			log.Error("Failed to resolve subscription login owner", "err", err)
			c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "subscription_login_unavailable"})
			return
		}
		completed, err := flow.CompleteLogin(c.Request.Context(), pending, request.Code)
		if errors.Is(err, subscriptions.ErrLoginPending) {
			c.JSON(http.StatusAccepted, gin.H{"status": "pending"})
			return
		}
		if err != nil {
			log.Warn("Subscription login was not completed", "err", err)
			c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "subscription_login_rejected"})
			return
		}
		account, err := authSvc.AddSubscriptionAccount(c.Request.Context(), auth.CreateSubscriptionAccountParams{
			Owner:             enrollmentOwner,
			Provider:          auth.SubscriptionProvider(completed.Provider),
			ExternalAccountID: completed.ExternalAccountID,
			DisplayName:       completed.DisplayName,
			RefreshToken:      []byte(completed.RefreshToken),
		})
		if err != nil {
			log.Warn("Subscription login enrollment rejected", "err", err)
			c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "subscription_account_rejected"})
			return
		}
		log.Info("Subscription connected from browser login", "account_id", account.ID)
		c.JSON(http.StatusCreated, internalSubscriptionAccountResponse{
			ID:                account.ID,
			Provider:          account.Provider,
			ExternalAccountID: account.ExternalAccountID,
			DisplayName:       account.DisplayName,
			Enabled:           account.Enabled,
			State:             account.State,
			CooldownUntil:     account.CooldownUntil,
			CreatedAt:         account.CreatedAt,
		})
	}
}

func sealSubscriptionLogin(authSvc *auth.Service, subscriberID string, pending subscriptions.PendingLogin) (string, error) {
	state, err := json.Marshal(pending)
	if err != nil {
		return "", err
	}
	sealed, err := authSvc.SealSubscriptionLogin(subscriberID, state)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(sealed), nil
}

func openSubscriptionLogin(authSvc *auth.Service, subscriberID, token string) (subscriptions.PendingLogin, error) {
	sealed, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return subscriptions.PendingLogin{}, err
	}
	state, err := authSvc.OpenSubscriptionLogin(subscriberID, sealed)
	if err != nil {
		return subscriptions.PendingLogin{}, err
	}
	var pending subscriptions.PendingLogin
	if err := json.Unmarshal(state, &pending); err != nil {
		return subscriptions.PendingLogin{}, err
	}
	return pending, nil
}
