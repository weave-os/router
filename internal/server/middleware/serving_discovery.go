package middleware

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/observability"
	"weave-os/router/internal/policyregistry"
)

// WithServingDiscovery is mounted only on private managed-worker discovery GETs.
// Keyless requests load the gateway's exact default snapshot without gaining inference identity.
func WithServingDiscovery(authSvc *auth.Service, byokRequiresOptIn bool, cfg *ServingAdmissionConfig) gin.HandlersChain {
	withAuth := WithAuth(authSvc, byokRequiresOptIn)
	withAdmission := WithServingAdmission(cfg)
	return gin.HandlersChain{
		func(c *gin.Context) {
			if extractToken(c) != "" {
				withAuth(c)
				return
			}
			selection, err := policyregistry.DecodeDiscoverySelection(c.GetHeader(policyregistry.DiscoverySelectionHeader))
			if err != nil {
				observability.FromGin(c).Debug("Worker discovery selection rejected", "err", err)
				c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "discovery_selection_required"})
				return
			}
			admission := policyregistry.SessionReleaseBinding{Target: selection.Target, Selection: selection.Selection}
			binding, err := policyregistry.ResolveAdmissionBinding(c.Request.Context(), cfg.Store, admission)
			if err != nil {
				observability.FromGin(c).Error("Worker discovery binding unavailable", "target", selection.Target, "release_sha256", selection.Selection.Release.SHA256, "err", err)
				c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{"error": "discovery_snapshot_unavailable"})
				return
			}
			if err := cfg.Identity.ValidateBinding(selection.Target, binding); err != nil {
				observability.FromGin(c).Warn("Worker discovery identity rejected", "target", selection.Target, "release_sha256", selection.Selection.Release.SHA256, "err", err)
				c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "discovery_selection_rejected"})
				return
			}
			snapshot, err := cfg.Cache.Snapshot(c.Request.Context(), admission)
			if err != nil {
				observability.FromGin(c).Error("Worker discovery snapshot unavailable", "target", selection.Target, "release_sha256", selection.Selection.Release.SHA256, "err", err)
				c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{"error": "discovery_snapshot_unavailable"})
				return
			}
			c.Request = c.Request.WithContext(policyregistry.WithServingSnapshot(c.Request.Context(), snapshot))
			c.Next()
		},
		func(c *gin.Context) {
			if extractToken(c) != "" {
				withAdmission(c)
				return
			}
			c.Next()
		},
	}
}
