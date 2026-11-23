package anthropic

import (
	"context"
	"errors"
	"io"
	"net/http"

	"weave-os/router/internal/ingress"
	"weave-os/router/internal/observability"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/policy"
	"weave-os/router/internal/server/middleware"

	"github.com/gin-gonic/gin"
)

type AnthropicRouter interface {
	RouteAnthropicRequest(ctx context.Context, body []byte, headers http.Header) (router.Decision, error)
}

func RouteHandler(svc AnthropicRouter) gin.HandlerFunc {
	return func(c *gin.Context) {
		log := observability.FromGin(c)

		body, err := io.ReadAll(io.LimitReader(c.Request.Body, requestcontext.MaxRequestBodyBytes+1))
		if err != nil {
			log.Debug("Failed to read request body", "err", err)
			writeAnthropicError(c, http.StatusBadRequest, "invalid_request_error", "Failed to read request body.")
			return
		}
		if len(body) > requestcontext.MaxRequestBodyBytes {
			logOversizeBody(log, c.Request)
			writeAnthropicError(c, http.StatusRequestEntityTooLarge, "invalid_request_error", "Request body too large.")
			return
		}

		ctx := c.Request.Context()
		decision, routeErr := svc.RouteAnthropicRequest(ctx, body, c.Request.Header)
		if routeErr != nil {
			if errors.Is(routeErr, ingress.ErrRequestNotJSONObject) {
				writeAnthropicError(c, http.StatusBadRequest, "invalid_request_error", "Request body must be a JSON object.")
				return
			}
			if errors.Is(routeErr, router.ErrInvalidRoutingKnobs) {
				log.Warn("Invalid routing knobs supplied on route", "err", routeErr)
				writeAnthropicError(c, http.StatusBadRequest, "invalid_request_error", "Invalid routing knobs supplied.")
				return
			}
			if errors.Is(routeErr, router.ErrPolicyPinUnavailable) {
				log.Warn("Policy pin unavailable on route", "err", routeErr)
				writeAnthropicError(c, http.StatusServiceUnavailable, "api_error", router.PolicyPinUnavailableReason)
				return
			}
			log.Error("Routing failed", "err", routeErr)
			writeAnthropicError(c, http.StatusBadGateway, "api_error", "Routing failed.")
			return
		}

		c.JSON(http.StatusOK, gin.H{
			"model":    decision.Model,
			"provider": decision.Provider,
			"reason":   decision.Reason,
		})
	}
}

type RoutePreviewer interface {
	PreviewAnthropicRoute(ctx context.Context, body []byte, headers http.Header) (policy.PreviewResult, error)
}

func PreviewRouteHandler(svc RoutePreviewer) gin.HandlerFunc {
	return func(c *gin.Context) {
		log := observability.FromGin(c)
		installation := middleware.InstallationFrom(c)
		if installation == nil || !installation.PolicyHeaderOverridesEnabled {
			writeAnthropicError(c, http.StatusForbidden, "permission_error", "Route preview isn't enabled for this installation.")
			return
		}
		strategy := router.StrategyFromContext(c.Request.Context())
		if strategy != router.StrategyHMM && strategy != router.StrategyHMMEmbedding {
			writeAnthropicError(c, http.StatusBadRequest, "invalid_request_error", "Route preview requires an HMM strategy.")
			return
		}

		body, err := io.ReadAll(io.LimitReader(c.Request.Body, requestcontext.MaxRequestBodyBytes+1))
		if err != nil {
			log.Debug("Failed to read route preview body", "err", err)
			writeAnthropicError(c, http.StatusBadRequest, "invalid_request_error", "Failed to read request body.")
			return
		}
		if len(body) > requestcontext.MaxRequestBodyBytes {
			logOversizeBody(log, c.Request)
			writeAnthropicError(c, http.StatusRequestEntityTooLarge, "invalid_request_error", "Request body too large.")
			return
		}

		result, previewErr := svc.PreviewAnthropicRoute(c.Request.Context(), body, c.Request.Header)
		if previewErr != nil {
			if errors.Is(previewErr, ingress.ErrRequestNotJSONObject) {
				writeAnthropicError(c, http.StatusBadRequest, "invalid_request_error", "Request body must be a JSON object.")
				return
			}
			if errors.Is(previewErr, router.ErrInvalidRoutingKnobs) {
				writeAnthropicError(c, http.StatusBadRequest, "invalid_request_error", "Invalid routing knobs supplied.")
				return
			}
			if errors.Is(previewErr, router.ErrPolicyPinUnavailable) {
				log.Warn("Policy pin unavailable on route preview", "err", previewErr)
				writeAnthropicError(c, http.StatusServiceUnavailable, "api_error", router.PolicyPinUnavailableReason)
				return
			}
			log.Error("Route preview failed", "err", previewErr)
			writeAnthropicError(c, http.StatusBadGateway, "api_error", "Route preview failed.")
			return
		}
		c.JSON(http.StatusOK, result)
	}
}
