package conversation

import (
	"context"
	"errors"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"

	"weave-os/router/internal/inference"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/policy"
	"weave-os/router/internal/router/sessionpin"
	"weave-os/router/internal/router/turntype"
	"weave-os/router/internal/routingpolicy"
)

const piHandoffTTL = 10 * time.Minute
const PiHandoffField = "weave_handoff"

const PiSessionField = "weave_session"
const piSessionIssuer = "weave-pi-session-v1"

type HandoffPurpose string

const (
	HandoffContinuation HandoffPurpose = "continuation"
	HandoffSummary      HandoffPurpose = "summary"
)

type handoffPreparationKey struct{}
type handoffClaimsKey struct{}
type piSessionClaimsKey struct{}

var ErrHandoffUnavailable = errors.New("pi handoff requires a shared ROUTER_PI_HANDOFF_SECRET of at least 32 bytes")

var ErrHandoffInvalid = errors.New("pi handoff expired or no longer matches this request; prepare the route again")

const (
	HandoffUnavailableMessage = "Pi handoff requires a shared ROUTER_PI_HANDOFF_SECRET of at least 32 bytes"
	HandoffInvalidMessage     = "Pi handoff expired or no longer matches this request; prepare the route again"
)

type HandoffClaims struct {
	jwt.RegisteredClaims
	Purpose        HandoffPurpose          `json:"purpose"`
	APIKeyID       string                  `json:"key"`
	SessionID      string                  `json:"session"`
	RequestedModel string                  `json:"requested_model"`
	Tools          string                  `json:"tools"`
	Reasoning      string                  `json:"reasoning"`
	Route          routingpolicy.TurnRoute `json:"route"`
}

type PiSessionClaims struct {
	jwt.RegisteredClaims
	APIKeyID              string                         `json:"key"`
	InstallationID        uuid.UUID                      `json:"installation"`
	SessionID             string                         `json:"session"`
	MetadataUserID        string                         `json:"user"`
	SessionKey            [sessionpin.SessionKeyLen]byte `json:"thread"`
	ServingStateNamespace string                         `json:"serving_state_namespace,omitempty"`
}

type PiHandoff struct {
	Secret []byte
	Now    func() time.Time
}

func WithHandoffPreparation(ctx context.Context) context.Context {
	return context.WithValue(ctx, handoffPreparationKey{}, true)
}

func PreparingHandoff(ctx context.Context) bool {
	preparing, _ := ctx.Value(handoffPreparationKey{}).(bool)
	return preparing
}

func HandoffFromContext(ctx context.Context) *HandoffClaims {
	claims, _ := ctx.Value(handoffClaimsKey{}).(*HandoffClaims)
	return claims
}

func PiSessionFromContext(ctx context.Context) *PiSessionClaims {
	claims, _ := ctx.Value(piSessionClaimsKey{}).(*PiSessionClaims)
	return claims
}

func (p PiHandoff) Parse(ctx context.Context, body []byte, installationID uuid.UUID) (context.Context, []byte, error) {
	ctx, body, err := p.parseSession(ctx, body, installationID)
	if err != nil {
		return ctx, body, err
	}
	encoded := gjson.GetBytes(body, PiHandoffField)
	if PreparingHandoff(ctx) && len(p.Secret) == 0 {
		return ctx, body, ErrHandoffUnavailable
	}
	if !encoded.Exists() {
		return ctx, body, nil
	}
	if len(p.Secret) == 0 || PreparingHandoff(ctx) || encoded.Type != gjson.String {
		return ctx, body, ErrHandoffInvalid
	}
	claims := &HandoffClaims{}
	_, err = jwt.ParseWithClaims(encoded.String(), claims, func(_ *jwt.Token) (any, error) {
		return p.Secret, nil
	}, jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}), jwt.WithExpirationRequired(), jwt.WithIssuer("weave-pi-handoff-v1"), jwt.WithTimeFunc(p.Now))
	apiKeyID, _ := ctx.Value(requestcontext.APIKeyIDContextKey{}).(string)
	if err != nil || apiKeyID == "" || claims.APIKeyID != apiKeyID ||
		claims.Route.InstallationID != installationID ||
		(claims.Purpose != HandoffContinuation && claims.Purpose != HandoffSummary) {
		return ctx, body, ErrHandoffInvalid
	}
	clean, err := sjson.DeleteBytes(body, PiHandoffField)
	return context.WithValue(ctx, handoffClaimsKey{}, claims), clean, err
}

func (p PiHandoff) Mint(claims HandoffClaims) (string, error) {
	claims.RegisteredClaims = jwt.RegisteredClaims{
		Issuer: "weave-pi-handoff-v1", ExpiresAt: jwt.NewNumericDate(p.Now().Add(piHandoffTTL)),
	}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(p.Secret)
}

func SummaryHandoff(claims HandoffClaims, previous sessionpin.Pin) HandoffClaims {
	claims.Purpose = HandoffSummary
	claims.Route.Decision = previous.Decision()
	claims.Route.Fresh = router.Decision{}
	claims.Route.HardPinned = true
	claims.Route.AuthoritativePerTurn = false
	claims.Route.TurnType = turntype.Compaction
	claims.Route.Purpose = inference.PurposeClientCompaction
	claims.Route.Origin = policy.OverrideSourceSession
	claims.Route.EscalationOrdinal = 0
	return claims
}

func (p PiHandoff) parseSession(ctx context.Context, body []byte, installationID uuid.UUID) (context.Context, []byte, error) {
	encoded := gjson.GetBytes(body, PiSessionField)
	if !encoded.Exists() {
		return ctx, body, nil
	}
	if len(p.Secret) == 0 || encoded.Type != gjson.String {
		return ctx, body, ErrHandoffInvalid
	}
	claims := &PiSessionClaims{}
	_, err := jwt.ParseWithClaims(encoded.String(), claims, func(_ *jwt.Token) (any, error) {
		return p.Secret, nil
	}, jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}), jwt.WithIssuer(piSessionIssuer))
	apiKeyID, _ := ctx.Value(requestcontext.APIKeyIDContextKey{}).(string)
	if err != nil || apiKeyID == "" || claims.APIKeyID != apiKeyID ||
		claims.InstallationID != installationID || claims.SessionID == "" ||
		claims.SessionKey == ([sessionpin.SessionKeyLen]byte{}) {
		return ctx, body, ErrHandoffInvalid
	}
	clean, err := sjson.DeleteBytes(body, PiSessionField)
	return context.WithValue(ctx, piSessionClaimsKey{}, claims), clean, err
}

func (p PiHandoff) MintSession(claims PiSessionClaims) (string, error) {
	claims.RegisteredClaims = jwt.RegisteredClaims{Issuer: piSessionIssuer}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(p.Secret)
}
