package admin

import (
	"encoding/json"
	"errors"
	"net/http"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/observability"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type internalRegisterSessionIssuerRequest struct {
	InstallationID      string          `json:"installation_id" binding:"required"`
	CredentialSubjectID string          `json:"credential_subject_id"`
	Name                string          `json:"name"`
	PublicJWK           json.RawMessage `json:"public_jwk" binding:"required"`
}

type internalSessionIssuerResponse struct {
	IssuerID string `json:"issuer_id"`
	KID      string `json:"kid"`
}

// InternalRegisterSessionIssuerHandler registers the control plane's Ed25519
// public JWK as a session issuer for an installation. Idempotent per key.
func InternalRegisterSessionIssuerHandler(authSvc *auth.Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		var req internalRegisterSessionIssuerRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "Installation ID and public JWK are required."})
			return
		}
		if req.CredentialSubjectID != "" {
			if _, err := uuid.Parse(req.CredentialSubjectID); err != nil {
				c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "Credential subject ID must be a UUID."})
				return
			}
		}
		var name *string
		if req.Name != "" {
			name = &req.Name
		}
		key, kid, err := authSvc.RegisterSessionIssuer(c.Request.Context(), req.InstallationID, req.CredentialSubjectID, name, req.PublicJWK)
		switch {
		case errors.Is(err, auth.ErrInvalidSessionIssuerKey):
			c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "Public JWK must be an Ed25519 public key."})
			return
		case errors.Is(err, auth.ErrSessionIssuerConflict):
			c.AbortWithStatusJSON(http.StatusConflict, gin.H{"error": "Public key is registered to another installation or credential subject."})
			return
		case errors.Is(err, auth.ErrSessionIssuerSubjectUnavailable):
			c.AbortWithStatusJSON(http.StatusConflict, gin.H{"error": "Credential subject is unavailable for session issuer registration."})
			return
		case err != nil:
			observability.FromGin(c).Error("Failed to register session issuer", "installation_id", req.InstallationID, "err", err)
			c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "Failed to register session issuer."})
			return
		}
		c.JSON(http.StatusOK, internalSessionIssuerResponse{IssuerID: key.ID, KID: kid})
	}
}

// InternalRevokeSessionIssuerHandler revokes an issuer and with it every
// session token it signed.
func InternalRevokeSessionIssuerHandler(authSvc *auth.Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		installationID, kid := c.Param("installationID"), c.Param("kid")
		err := authSvc.RevokeSessionIssuer(c.Request.Context(), installationID, kid)
		if errors.Is(err, auth.ErrAPIKeyNotFound) {
			c.AbortWithStatusJSON(http.StatusNotFound, gin.H{"error": "Session issuer not found."})
			return
		}
		if err != nil {
			observability.FromGin(c).Error("Failed to revoke session issuer", "installation_id", installationID, "kid", kid, "err", err)
			c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "Failed to revoke session issuer."})
			return
		}
		c.Status(http.StatusNoContent)
	}
}
