package middleware

import (
	"strings"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/flags"
	"weave-os/router/internal/observability"
	"weave-os/router/internal/refusal"

	"github.com/gin-gonic/gin"
)

const FlagOverridesHeader = "x-weave-flag-overrides"

func WithFlagOverridesHeader(svc *auth.Service) gin.HandlerFunc {
	orgOverridesDisabled := svc != nil && svc.FlagOverridesDisabled()
	return func(c *gin.Context) {
		raw := strings.TrimSpace(c.GetHeader(FlagOverridesHeader))
		if raw == "" {
			c.Next()
			return
		}
		installation := InstallationFrom(c)
		if orgOverridesDisabled || installation == nil || !installation.PolicyHeaderOverridesEnabled {
			observability.FromGin(c).Debug("Flag overrides header ignored: installation is not authorized for policy headers")
			c.Next()
			return
		}
		requested, err := flags.DecodeOverrides([]byte(raw))
		if err != nil {
			abortRefusal(c, refusal.FlagOverridesMalformed, gin.H{"message": err.Error()})
			return
		}
		ctx := c.Request.Context()
		stored, _ := flags.OverridesFromContext(ctx)
		effective := stored.Overlay(requested)
		if err := flags.ValidateOverrides(effective); err != nil {
			abortRefusal(c, refusal.FlagOverridesMalformed, gin.H{"message": err.Error()})
			return
		}
		observability.FromGin(c).Debug("Flag overrides header applied", "installation_id", installation.ID, "flag_keys", requested.Keys())
		c.Request = c.Request.WithContext(flags.WithRequestOverlay(ctx, stored, effective))
		c.Next()
	}
}
