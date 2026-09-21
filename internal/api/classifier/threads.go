// Package classifier exposes the authenticated, explicit new-thread handshake.
package classifier

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"weave-os/router/internal/observability"
	"weave-os/router/internal/proxy"
	"weave-os/router/internal/router"
)

// StartThreadHandler requires a fresh idempotency UUID for each new thread.
// A client must retain that UUID for handshake retries, never for child threads.
func StartThreadHandler(service *proxy.Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("Cache-Control", "no-store")
		var startRequest struct {
			NewChatID uuid.UUID `json:"new_chat_id"`
		}
		decoder := json.NewDecoder(http.MaxBytesReader(c.Writer, c.Request.Body, 1024))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&startRequest); err != nil || startRequest.NewChatID == uuid.Nil {
			// Strict-decoder errors may echo arbitrary input field names.
			observability.FromGin(c).Debug("Invalid classifier handshake UUID or JSON", "path", c.FullPath())
			c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "invalid_new_chat_id"})
			return
		}
		if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
			observability.FromGin(c).Debug("Classifier handshake has trailing JSON", "path", c.FullPath())
			c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "invalid_request"})
			return
		}
		ticket, err := service.StartClassifierThread(c.Request.Context(), startRequest.NewChatID)
		if err != nil {
			observability.FromGin(c).Warn("Classifier thread handshake failed", "new_chat_id", startRequest.NewChatID, "err", err)
			status := http.StatusServiceUnavailable
			if errors.Is(err, router.ErrClassifierThreadInvalid) {
				status = http.StatusForbidden
			}
			c.AbortWithStatusJSON(status, gin.H{"error": "classifier_thread_unavailable"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"thread_token": ticket, "header": proxy.ClassifierThreadHeader})
	}
}
