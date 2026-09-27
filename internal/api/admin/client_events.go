package admin

import (
	"net/http"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/observability"
	"weave-os/router/internal/server/middleware"

	"github.com/gin-gonic/gin"
)

type clientEventRequest struct {
	Action  auth.HarnessLifecycleAction `json:"action"`
	Harness auth.LifecycleHarness       `json:"harness"`
}

// ClientEventHandler accepts a harness CLI's report that a local off/on/
// uninstall already succeeded. The CLI fires it fail-open, so this only logs
// and exports the event; nothing is persisted.
func ClientEventHandler(authSvc *auth.Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		installation := middleware.InstallationFrom(c)
		if installation == nil {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "invalid_key"})
			return
		}
		var req clientEventRequest
		if err := c.ShouldBindJSON(&req); err != nil || !req.Action.Valid() || !req.Harness.Valid() {
			c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "invalid_body"})
			return
		}
		apiKey := middleware.APIKeyFrom(c)
		log := observability.FromGin(c).With("client_event", "harness_lifecycle", "action", string(req.Action), "harness", string(req.Harness),
			"installation_id", installation.ID, "external_id", installation.ExternalID)
		if apiKey != nil {
			log = log.With("router_api_key_id", apiKey.ID)
			if apiKey.CredentialSubjectID != "" {
				log = log.With("credential_subject_id", apiKey.CredentialSubjectID)
			}
		}
		log.Info("Harness lifecycle event reported by client")
		authSvc.ReportHarnessLifecycle(installation, apiKey, req.Harness, req.Action)
		c.Status(http.StatusNoContent)
	}
}
