package admin

import (
	"encoding/hex"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"weave-os/router/internal/flags"
	"weave-os/router/internal/observability"
	"weave-os/router/internal/proxy"
	"weave-os/router/internal/router/escalationdashboard"
	"weave-os/router/internal/router/llmescalation"
)

func InternalEscalationDashboardHandler(service *proxy.Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		filter, ok := escalationDashboardFilter(c)
		if !ok {
			return
		}
		snapshot, err := service.EscalationDashboard(c.Request.Context(), filter)
		if err != nil {
			internalEscalationError(c, err)
			return
		}
		c.JSON(http.StatusOK, snapshot)
	}
}

func escalationDashboardFilter(c *gin.Context) (escalationdashboard.Filter, bool) {
	limit, limitOK := queryInteger(c, "limit", 50)
	if !limitOK || limit < 1 || limit > 200 {
		c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "Invalid pagination."})
		return escalationdashboard.Filter{}, false
	}
	installationID := strings.TrimSpace(c.Query("installation_id"))
	if installationID != "" {
		parsedInstallationID, err := uuid.Parse(installationID)
		if err != nil {
			c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "Invalid installation ID."})
			return escalationdashboard.Filter{}, false
		}
		installationID = parsedInstallationID.String()
	}
	filter := escalationdashboard.Filter{
		Service:        escalationdashboard.Service(c.Query("service")),
		Mode:           escalationdashboard.Mode(c.Query("mode")),
		OrganizationID: strings.TrimSpace(c.Query("organization_id")),
		InstallationID: installationID,
		SessionOutcome: escalationdashboard.SessionOutcome(c.Query("outcome")),
		Limit:          limit,
		Cursor:         strings.TrimSpace(c.Query("cursor")),
	}
	if !validEscalationDashboardFilter(filter) {
		c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "Invalid escalation dashboard filter."})
		return escalationdashboard.Filter{}, false
	}
	return filter, true
}

func validEscalationDashboardFilter(filter escalationdashboard.Filter) bool {
	validService := filter.Service == "" || filter.Service == escalationdashboard.ServiceXGB || filter.Service == escalationdashboard.ServiceSwitchyard
	validMode := filter.Mode == "" || filter.Mode == escalationdashboard.ModeActive || filter.Mode == escalationdashboard.ModeShadow || filter.Mode == escalationdashboard.ModeUnknown
	validOutcome := filter.SessionOutcome == "" || filter.SessionOutcome == escalationdashboard.SessionOutcomeRecommended || filter.SessionOutcome == escalationdashboard.SessionOutcomeApplied || filter.SessionOutcome == escalationdashboard.SessionOutcomeShadow || filter.SessionOutcome == escalationdashboard.SessionOutcomeNoEvaluation
	return validService && validMode && validOutcome
}

func InternalLLMEscalationSessionsHandler(service *proxy.Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		installationID := strings.TrimSpace(c.Query("installation_id"))
		if installationID != "" {
			if _, err := uuid.Parse(installationID); err != nil {
				c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "Invalid installation ID."})
				return
			}
		}
		limit, limitOK := queryInteger(c, "limit", 50)
		offset, offsetOK := queryInteger(c, "offset", 0)
		if !limitOK || !offsetOK || limit < 1 || limit > 200 || offset < 0 {
			c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "Invalid pagination."})
			return
		}
		snapshot, err := service.ListLLMEscalationSessions(c.Request.Context(), installationID, limit, offset)
		if err != nil {
			internalEscalationError(c, err)
			return
		}
		c.JSON(http.StatusOK, snapshot)
	}
}

func InternalLLMEscalationSessionHandler(service *proxy.Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		installationID := strings.TrimSpace(c.Query("installation_id"))
		if _, err := uuid.Parse(installationID); err != nil {
			c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "Invalid installation ID."})
			return
		}
		decodedScope, err := hex.DecodeString(c.Param("scope"))
		if err != nil || len(decodedScope) != 32 {
			c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "Invalid escalation scope."})
			return
		}
		detail, found, err := service.GetLLMEscalationSession(c.Request.Context(), installationID, c.Param("scope"))
		if err != nil {
			internalEscalationError(c, err)
			return
		}
		if !found {
			c.AbortWithStatus(http.StatusNotFound)
			return
		}
		c.JSON(http.StatusOK, detail)
	}
}

func InternalEscalationConfigurationHandler(service *proxy.Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		installationID := c.Param("installationID")
		if _, err := uuid.Parse(installationID); err != nil {
			c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "Invalid installation ID."})
			return
		}
		selection, err := service.EscalationSelection(c.Request.Context(), installationID)
		if err != nil {
			internalEscalationError(c, err)
			return
		}
		c.JSON(http.StatusOK, selection)
	}
}

func InternalUpdateEscalationConfigurationHandler(service *proxy.Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		installationID := c.Param("installationID")
		if _, err := uuid.Parse(installationID); err != nil {
			c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "Invalid installation ID."})
			return
		}
		var update llmescalation.SelectionUpdate
		if err := c.ShouldBindJSON(&update); err != nil {
			c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "Invalid escalation configuration."})
			return
		}
		selection, err := service.UpdateEscalationSelection(c.Request.Context(), installationID, update)
		if err != nil {
			internalEscalationError(c, err)
			return
		}
		c.JSON(http.StatusOK, selection)
	}
}

func queryInteger(c *gin.Context, name string, defaultValue int32) (int32, bool) {
	raw := c.Query(name)
	if raw == "" {
		return defaultValue, true
	}
	value, err := strconv.ParseInt(raw, 10, 32)
	return int32(value), err == nil
}

func internalEscalationError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, llmescalation.ErrInstallationNotFound):
		c.AbortWithStatus(http.StatusNotFound)
	case errors.Is(err, llmescalation.ErrConfigurationConflict):
		c.AbortWithStatusJSON(http.StatusConflict, gin.H{"error": "Escalation configuration changed."})
	case errors.Is(err, flags.ErrInvalidValue):
		c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "Invalid escalation configuration."})
	case errors.Is(err, proxy.ErrEscalationJudgeUnavailable):
		c.AbortWithStatusJSON(http.StatusUnprocessableEntity, gin.H{"error": "Escalation judge is unavailable."})
	case errors.Is(err, escalationdashboard.ErrInvalidCursor):
		c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "Invalid escalation dashboard cursor."})
	case errors.Is(err, escalationdashboard.ErrExpiredCursor):
		c.AbortWithStatusJSON(http.StatusGone, gin.H{"error": "Escalation dashboard snapshot expired."})
	default:
		observability.FromGin(c).Error("Internal escalation operation failed", "err", err)
		c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "Escalation operation failed."})
	}
}
