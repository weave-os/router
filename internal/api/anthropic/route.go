package anthropic

import (
	"errors"
	"io"
	"net/http"

	"workweave/router/internal/observability"
	"workweave/router/internal/proxy"
	"workweave/router/internal/router"
	"workweave/router/internal/router/cluster"

	"github.com/gin-gonic/gin"
)

// RouteSchemaVersionV1 is the wire contract of the POST /v1/route response.
// Clients (the Go and Python route SDKs) pin on it to detect a breaking shape
// change; bump it whenever a field is removed or its meaning changes, and add
// only additive fields within a version.
const RouteSchemaVersionV1 = "router_route_v1"

func RouteHandler(svc *proxy.Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		log := observability.FromGin(c)

		body, err := io.ReadAll(io.LimitReader(c.Request.Body, proxy.MaxRequestBodyBytes+1))
		if err != nil {
			log.Debug("Failed to read request body", "err", err)
			writeAnthropicError(c, http.StatusBadRequest, "invalid_request_error", "Failed to read request body.")
			return
		}
		if len(body) > proxy.MaxRequestBodyBytes {
			writeAnthropicError(c, http.StatusRequestEntityTooLarge, "invalid_request_error", "Request body too large.")
			return
		}

		ctx := c.Request.Context()
		decision, routeErr := svc.RouteAnthropicRequest(ctx, body, c.Request.Header)
		if routeErr != nil {
			if errors.Is(routeErr, proxy.ErrRequestNotJSONObject) {
				writeAnthropicError(c, http.StatusBadRequest, "invalid_request_error", "Request body must be a JSON object.")
				return
			}
			if errors.Is(routeErr, cluster.ErrInvalidRoutingKnobs) {
				log.Warn("Invalid routing knobs supplied on route", "err", routeErr)
				writeAnthropicError(c, http.StatusBadRequest, "invalid_request_error", "Invalid routing knobs supplied.")
				return
			}
			log.Error("Routing failed", "err", routeErr)
			writeAnthropicError(c, http.StatusBadGateway, "api_error", "Routing failed.")
			return
		}

		c.JSON(http.StatusOK, gin.H{
			"schema_version": RouteSchemaVersionV1,
			"model":          decision.Model,
			"provider":       decision.Provider,
			"reason":         decision.Reason,
		})
	}
}

// PreviewRouteHandler exposes the side-effect-free policy preview contract. It
// requires a valid rk_ bearer token (applied via the route group's auth
// middleware) and an HMM strategy header; the preview is genuinely HMM-shaped
// (hmm_state_id, class_probabilities) and requires a running sidecar.
func PreviewRouteHandler(svc *proxy.Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		log := observability.FromGin(c)
		strategy := router.StrategyFromContext(c.Request.Context())
		if strategy != router.StrategyHMM && strategy != router.StrategyHMMEmbedding {
			writeAnthropicError(c, http.StatusBadRequest, "invalid_request_error", "Route preview requires an HMM strategy.")
			return
		}

		body, err := io.ReadAll(io.LimitReader(c.Request.Body, proxy.MaxRequestBodyBytes+1))
		if err != nil {
			log.Debug("Failed to read route preview body", "err", err)
			writeAnthropicError(c, http.StatusBadRequest, "invalid_request_error", "Failed to read request body.")
			return
		}
		if len(body) > proxy.MaxRequestBodyBytes {
			writeAnthropicError(c, http.StatusRequestEntityTooLarge, "invalid_request_error", "Request body too large.")
			return
		}

		result, previewErr := svc.PreviewAnthropicRoute(c.Request.Context(), body, c.Request.Header)
		if previewErr != nil {
			if errors.Is(previewErr, proxy.ErrRequestNotJSONObject) {
				writeAnthropicError(c, http.StatusBadRequest, "invalid_request_error", "Request body must be a JSON object.")
				return
			}
			if errors.Is(previewErr, cluster.ErrInvalidRoutingKnobs) {
				writeAnthropicError(c, http.StatusBadRequest, "invalid_request_error", "Invalid routing knobs supplied.")
				return
			}
			log.Error("Route preview failed", "err", previewErr)
			writeAnthropicError(c, http.StatusBadGateway, "api_error", "Route preview failed.")
			return
		}
		c.JSON(http.StatusOK, result)
	}
}
