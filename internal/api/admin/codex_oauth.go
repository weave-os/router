package admin

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"workweave/router/internal/providers"
)

type codexOAuthResponse struct {
	State   string `json:"state"`
	LoginID string `json:"login_id,omitempty"`
	AuthURL string `json:"auth_url,omitempty"`
	Error   string `json:"error,omitempty"`
}

func codexOAuthStatusResponse(status providers.CodexOAuthStatus) codexOAuthResponse {
	return codexOAuthResponse{
		State:   status.State,
		LoginID: status.LoginID,
		AuthURL: status.AuthURL,
		Error:   status.Error,
	}
}

// CodexOAuthStatusHandler returns the dashboard-safe status of the local
// Codex OAuth session.
func CodexOAuthStatusHandler(login providers.CodexOAuthLogin) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.JSON(http.StatusOK, codexOAuthStatusResponse(login.Status(c.Request.Context())))
	}
}

// CodexOAuthStartHandler starts the browser-based Codex OAuth flow.
func CodexOAuthStartHandler(login providers.CodexOAuthLogin) gin.HandlerFunc {
	return func(c *gin.Context) {
		started, err := login.Start(c.Request.Context())
		if err != nil {
			c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, codexOAuthResponse{
			State:   "pending",
			LoginID: started.LoginID,
			AuthURL: started.AuthURL,
		})
	}
}

// CodexOAuthCancelHandler cancels the active local Codex OAuth flow.
func CodexOAuthCancelHandler(login providers.CodexOAuthLogin) gin.HandlerFunc {
	return func(c *gin.Context) {
		if err := login.Cancel(c.Request.Context()); err != nil {
			c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
			return
		}
		c.Status(http.StatusNoContent)
	}
}
