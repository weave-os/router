package admin

import (
	"errors"
	"net/http"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/observability"
	"weave-os/router/internal/proxy"

	"github.com/gin-gonic/gin"
)

// CaptureModeSource reports the deployment-wide content-capture setting.
// Implemented by *proxy.Service.
type CaptureModeSource interface {
	CaptureMode() proxy.ContentCaptureMode
}

// contentCaptureResponse shows deployment, installation, and effective capture
// modes so the ceiling is visible rather than inferred.
type contentCaptureResponse struct {
	Deployment   string  `json:"deployment"`
	Installation *string `json:"installation"`
	Effective    string  `json:"effective"`
}

// updateContentCaptureRequest carries the requested ceiling. A nil Mode clears
// the override so the deployment setting applies unmodified.
type updateContentCaptureRequest struct {
	Mode *string `json:"mode"`
}

func contentCaptureFor(installation *auth.Installation, deployment proxy.ContentCaptureMode) contentCaptureResponse {
	effective := deployment
	if installation.ContentCaptureMode != nil {
		effective = proxy.StricterCaptureMode(deployment, proxy.ParseCaptureMode(*installation.ContentCaptureMode))
	}
	return contentCaptureResponse{
		Deployment:   deployment.String(),
		Installation: installation.ContentCaptureMode,
		Effective:    effective.String(),
	}
}

// GetContentCaptureHandler returns the deployment capture mode, the
// installation's override, and the resulting effective mode.
func GetContentCaptureHandler(authSvc *auth.Service, capture CaptureModeSource) gin.HandlerFunc {
	return func(c *gin.Context) {
		installation, ok := resolveInstallation(c, authSvc)
		if !ok {
			return
		}
		c.JSON(http.StatusOK, contentCaptureFor(installation, capture.CaptureMode()))
	}
}

// UpdateContentCaptureHandler persists the installation's capture ceiling.
// 400 on a mode outside off/hashed/full.
func UpdateContentCaptureHandler(authSvc *auth.Service, capture CaptureModeSource) gin.HandlerFunc {
	return func(c *gin.Context) {
		log := observability.FromGin(c)
		installation, ok := resolveInstallation(c, authSvc)
		if !ok {
			return
		}

		var req updateContentCaptureRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "Invalid request body."})
			return
		}

		if err := authSvc.SetInstallationContentCaptureMode(c.Request.Context(), installation.ExternalID, installation.ID, req.Mode); err != nil {
			if errors.Is(err, auth.ErrInvalidCaptureMode) {
				c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": err.Error()})
				return
			}
			log.Error("Failed to update content capture mode", "err", err, "installation_id", installation.ID)
			c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "Failed to update content capture mode."})
			return
		}

		installation.ContentCaptureMode = req.Mode
		c.JSON(http.StatusOK, contentCaptureFor(installation, capture.CaptureMode()))
	}
}

var _ CaptureModeSource = (*proxy.Service)(nil)
