package proxy

import (
	"context"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"

	"weave-os/router/internal/router/sessionpin"
	"weave-os/router/internal/translate"
)

const piSessionField = "weave_session"
const piSessionIssuer = "weave-pi-session-v1"

type piSessionClaimsKey struct{}

// This ticket preserves a thread digest, not a model selection or authorization.
// It lasts for the conversation; every use still requires the bound API key and
// installation. Rotating the signing secret invalidates it.
type piSessionClaims struct {
	jwt.RegisteredClaims
	APIKeyID       string                         `json:"key"`
	InstallationID uuid.UUID                      `json:"installation"`
	SessionID      string                         `json:"session"`
	MetadataUserID string                         `json:"user"`
	SessionKey     [sessionpin.SessionKeyLen]byte `json:"thread"`
}

func piSessionFromContext(ctx context.Context) *piSessionClaims {
	claims, _ := ctx.Value(piSessionClaimsKey{}).(*piSessionClaims)
	return claims
}

func (claims *piSessionClaims) matches(ctx context.Context, env *translate.RequestEnvelope) bool {
	return env != nil && claims.InstallationID == installationIDFromContext(ctx) &&
		claims.SessionID == clientSessionIDForRequest(ctx, env) && claims.MetadataUserID == env.MetadataUserID()
}

func (s *Service) parsePiSession(ctx context.Context, body []byte) (context.Context, []byte, error) {
	encoded := gjson.GetBytes(body, piSessionField)
	if !encoded.Exists() {
		return ctx, body, nil
	}
	if len(s.piHandoffSecret) == 0 || encoded.Type != gjson.String {
		return ctx, body, ErrHandoffInvalid
	}
	claims := &piSessionClaims{}
	_, err := jwt.ParseWithClaims(encoded.String(), claims, func(_ *jwt.Token) (any, error) {
		return s.piHandoffSecret, nil
	}, jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}), jwt.WithIssuer(piSessionIssuer))
	apiKeyID, _ := ctx.Value(APIKeyIDContextKey{}).(string)
	if err != nil || apiKeyID == "" || claims.APIKeyID != apiKeyID ||
		claims.InstallationID != installationIDFromContext(ctx) || claims.SessionID == "" ||
		claims.SessionKey == ([sessionpin.SessionKeyLen]byte{}) {
		return ctx, body, ErrHandoffInvalid
	}
	clean, err := sjson.DeleteBytes(body, piSessionField)
	return context.WithValue(ctx, piSessionClaimsKey{}, claims), clean, err
}

func (s *Service) mintPiSession(ctx context.Context, env *translate.RequestEnvelope, sessionKey [sessionpin.SessionKeyLen]byte) (string, error) {
	apiKeyID, _ := ctx.Value(APIKeyIDContextKey{}).(string)
	claims := piSessionClaims{
		RegisteredClaims: jwt.RegisteredClaims{Issuer: piSessionIssuer},
		APIKeyID:         apiKeyID, InstallationID: installationIDFromContext(ctx),
		SessionID: clientSessionIDForRequest(ctx, env), MetadataUserID: env.MetadataUserID(), SessionKey: sessionKey,
	}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(s.piHandoffSecret)
}
