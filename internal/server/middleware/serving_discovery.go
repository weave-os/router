package middleware

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"weave-os/router/internal/observability"
	"weave-os/router/internal/policyregistry"
)

// WithServingDiscovery is mounted only on private managed-worker discovery GETs.
// Requests load the gateway's exact snapshot without gaining inference identity.
func WithServingDiscovery(cfg *ServingAdmissionConfig) gin.HandlerFunc {
	return func(c *gin.Context) {
		selection, err := policyregistry.DecodeDiscoverySelection(c.GetHeader(policyregistry.DiscoverySelectionHeader))
		if err != nil {
			observability.FromGin(c).Debug("Worker discovery selection rejected", "err", err)
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "discovery_selection_required"})
			return
		}
		admission := policyregistry.SessionReleaseBinding{Target: selection.Target, ProfileKey: selection.ProfileKey, Selection: selection.Selection}
		log := observability.FromGin(c).With("target", selection.Target, "profile_key", selection.ProfileKey, "release_sha256", selection.Selection.Release.SHA256)
		binding, err := policyregistry.ResolveAdmissionBinding(c.Request.Context(), cfg.Store, admission)
		if err != nil {
			log.Error("Worker discovery binding unavailable", "err", err)
			c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{"error": "discovery_snapshot_unavailable"})
			return
		}
		if err := cfg.Identity.ValidateBinding(selection.Target, binding); err != nil {
			log.Warn("Worker discovery identity rejected", "err", err)
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "discovery_selection_rejected"})
			return
		}
		snapshot, err := cfg.Cache.Snapshot(c.Request.Context(), admission)
		if err != nil {
			log.Error("Worker discovery snapshot unavailable", "err", err)
			c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{"error": "discovery_snapshot_unavailable"})
			return
		}
		c.Request = c.Request.WithContext(policyregistry.WithServingSnapshot(c.Request.Context(), snapshot))
		c.Next()
	}
}
