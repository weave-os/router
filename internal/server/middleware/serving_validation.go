package middleware

import (
	"encoding/json"
	"io"
	"net/http"

	"github.com/gin-gonic/gin"
	"weave-os/router/internal/observability"
	"weave-os/router/internal/policyregistry"
)

// ServingValidationHandler is mounted only on the private managed worker.
// Cloud Run IAM admits validation identities; no routing key or signing secret is issued to them.
func ServingValidationHandler(cfg *ServingAdmissionConfig) gin.HandlerFunc {
	return func(c *gin.Context) {
		var request policyregistry.WorkerValidationRequest
		decoder := json.NewDecoder(http.MaxBytesReader(c.Writer, c.Request.Body, 64*1024))
		decoder.DisallowUnknownFields()
		err := decoder.Decode(&request)
		if err != nil {
			observability.FromGin(c).Debug("Worker validation JSON rejected", "err", err)
			c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "invalid_validation_request"})
			return
		}
		if err := decoder.Decode(new(any)); err != io.EOF {
			observability.FromGin(c).Debug("Worker validation trailing JSON rejected", "err", err)
			c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "invalid_validation_request"})
			return
		}
		attestation, err := policyregistry.ValidateWorkerSelection(c.Request.Context(), cfg.Store, cfg.Cache, cfg.Identity, request)
		if err != nil {
			observability.FromGin(c).Warn("Managed worker preparation rejected", "target", request.Target, "profile_key", request.ProfileKey, "release_sha256", request.Selection.Release.SHA256, "err", err)
			c.AbortWithStatusJSON(http.StatusUnprocessableEntity, gin.H{"error": "worker_preparation_failed"})
			return
		}
		c.JSON(http.StatusOK, attestation)
	}
}
