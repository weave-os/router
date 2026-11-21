package feedback

import (
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"weave-os/router/internal/feedback"
	"weave-os/router/internal/observability"
	"weave-os/router/internal/proxy"

	"github.com/gin-gonic/gin"
)

const maxBodyBytes = 64 * 1024

type submittedFeedback struct {
	Rating  string  `json:"rating"`
	Comment *string `json:"comment,omitempty"`
}

type contextResponse struct {
	RequestID      string             `json:"request_id"`
	ChosenModel    string             `json:"chosen_model,omitempty"`
	ChosenProvider string             `json:"chosen_provider,omitempty"`
	ClientApp      string             `json:"client_app,omitempty"`
	RoutedAt       string             `json:"routed_at,omitempty"`
	Feedback       *submittedFeedback `json:"feedback"`
}

type submitRequest struct {
	Token   string  `json:"token"`
	Rating  string  `json:"rating"`
	Comment *string `json:"comment"`
}

func GetContextHandler(svc *proxy.Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		log := observability.FromGin(c)

		claims, err := svc.VerifyFeedbackToken(c.Param("token"))
		if err != nil {
			writeTokenError(c, err)
			return
		}

		fctx, err := svc.GetFeedbackContext(c.Request.Context(), claims.InstallationID, claims.RequestID)
		if errors.Is(err, sql.ErrNoRows) {
			c.JSON(http.StatusOK, contextResponse{RequestID: claims.RequestID})
			return
		}
		if err != nil {
			log.Error("Failed to load feedback context", "err", err, "rated_request_id", claims.RequestID)
			c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "failed to load context"})
			return
		}
		c.JSON(http.StatusOK, toContextResponse(fctx))
	}
}

func SubmitHandler(svc *proxy.Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		log := observability.FromGin(c)

		body, err := io.ReadAll(io.LimitReader(c.Request.Body, maxBodyBytes+1))
		if err != nil || len(body) > maxBodyBytes {
			c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "invalid_request"})
			return
		}
		var req submitRequest
		if jsonErr := json.Unmarshal(body, &req); jsonErr != nil {
			c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "invalid_request"})
			return
		}
		if req.Rating != "up" && req.Rating != "down" {
			c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "invalid_rating"})
			return
		}

		claims, err := svc.VerifyFeedbackToken(req.Token)
		if err != nil {
			writeTokenError(c, err)
			return
		}

		err = svc.SubmitFeedback(c.Request.Context(), feedback.SubmitFeedbackParams{
			InstallationID: claims.InstallationID,
			ExternalID:     claims.ExternalID,
			RequestID:      claims.RequestID,
			RouterUserID:   claims.RouterUserID,
			Rating:         req.Rating,
			Comment:        normalizeComment(req.Comment),
		})
		if err != nil {
			log.Error("Failed to submit feedback", "err", err, "rated_request_id", claims.RequestID)
			c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "failed to submit feedback"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	}
}

func RateHandler(svc *proxy.Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		log := observability.FromGin(c)

		rating := c.Query("r")
		if rating != "up" && rating != "down" {
			c.Data(http.StatusBadRequest, "text/html; charset=utf-8", ratePageError("That rating link looks malformed."))
			return
		}

		claims, err := svc.VerifyFeedbackToken(c.Query("t"))
		if err != nil {
			msg := "This feedback link is invalid."
			if errors.Is(err, feedback.ErrExpiredToken) {
				msg = "This feedback link has expired."
			}
			status := http.StatusNotFound
			if errors.Is(err, feedback.ErrExpiredToken) {
				status = http.StatusGone
			}
			c.Data(status, "text/html; charset=utf-8", ratePageError(msg))
			return
		}

		err = svc.SubmitFeedback(c.Request.Context(), feedback.SubmitFeedbackParams{
			InstallationID: claims.InstallationID,
			ExternalID:     claims.ExternalID,
			RequestID:      claims.RequestID,
			RouterUserID:   claims.RouterUserID,
			Rating:         rating,
		})
		if err != nil {
			log.Error("Failed to record one-click feedback", "err", err, "rated_request_id", claims.RequestID, "rating", rating)
			c.Data(http.StatusInternalServerError, "text/html; charset=utf-8", ratePageError("Sorry — we couldn't record that. Please try again."))
			return
		}

		c.Data(http.StatusOK, "text/html; charset=utf-8", ratePageSuccess())
	}
}

func toContextResponse(f feedback.FeedbackContext) contextResponse {
	resp := contextResponse{
		RequestID:      f.RequestID,
		ChosenModel:    f.ChosenModel,
		ChosenProvider: f.ChosenProvider,
		ClientApp:      f.ClientApp,
	}
	if !f.RoutedAt.IsZero() {
		resp.RoutedAt = f.RoutedAt.UTC().Format(time.RFC3339)
	}
	if f.Rating != "" {
		resp.Feedback = &submittedFeedback{Rating: f.Rating, Comment: f.Comment}
	}
	return resp
}

func normalizeComment(comment *string) *string {
	if comment == nil {
		return nil
	}
	trimmed := strings.TrimSpace(*comment)
	if trimmed == "" {
		return nil
	}
	return &trimmed
}

func writeTokenError(c *gin.Context, err error) {
	if errors.Is(err, feedback.ErrExpiredToken) {
		c.AbortWithStatusJSON(http.StatusGone, gin.H{"error": "expired"})
		return
	}
	c.AbortWithStatusJSON(http.StatusNotFound, gin.H{"error": "invalid"})
}
