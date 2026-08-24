package auth

import (
	"context"
	"errors"
)

// EntraScope is the Microsoft Entra scope used by Azure AI inference endpoints.
const EntraScope = "https://ai.azure.com/.default"

// ErrEntraUnavailable is returned when an Azure Entra token cannot be obtained.
var ErrEntraUnavailable = errors.New("auth: Microsoft Entra token is unavailable")

// EntraTokenSource mints a short-lived Microsoft Entra token for a BYOK key.
// The key carries the tenant ID, client ID, and encrypted client secret.
type EntraTokenSource interface {
	Token(context.Context, *ExternalAPIKey) ([]byte, error)
}
