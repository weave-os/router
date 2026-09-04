package proxy_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/proxy"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func identityCtx(creds *proxy.Credentials, identity proxy.ClientIdentity) context.Context {
	ctx := context.WithValue(context.Background(), proxy.CredentialsContextKey{}, creds)
	return context.WithValue(ctx, proxy.ClientIdentityContextKey{}, identity)
}

func TestApplyIdentityHeader(t *testing.T) {
	identity := proxy.ClientIdentity{
		Email:       "engineer@example.com",
		DisplayName: "Engineer, Staff",
		SessionID:   "session-1",
		ClientApp:   "claude_code",
	}

	t.Run("sends nothing when the key configures no header", func(t *testing.T) {
		upstream := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
		proxy.ApplyIdentityHeader(identityCtx(&proxy.Credentials{}, identity), upstream)
		assert.Empty(t, upstream.Header,
			"a key that asked for nothing must not leak the caller's address to its endpoint")
	})

	t.Run("sends the bare address in email format", func(t *testing.T) {
		upstream := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
		creds := &proxy.Credentials{IdentityHeader: "X-Caller-Identity", IdentityHeaderFormat: auth.IdentityFormatEmail}
		proxy.ApplyIdentityHeader(identityCtx(creds, identity), upstream)
		assert.Equal(t, "engineer@example.com", upstream.Header.Get("X-Caller-Identity"))
	})

	t.Run("sends a URL-encoded property bag in json format", func(t *testing.T) {
		upstream := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
		creds := &proxy.Credentials{IdentityHeader: "X-Caller-Identity", IdentityHeaderFormat: auth.IdentityFormatJSON}
		proxy.ApplyIdentityHeader(identityCtx(creds, identity), upstream)

		raw := upstream.Header.Get("X-Caller-Identity")
		assert.NotContains(t, raw, ",",
			"an unencoded display name would break the header's grammar at the first comma")
		assert.NotContains(t, raw, "+",
			"a form-encoded space decodes to a literal '+' under decodeURIComponent, corrupting the name")
		decoded, err := url.QueryUnescape(raw)
		require.NoError(t, err)
		var bag map[string]string
		require.NoError(t, json.Unmarshal([]byte(decoded), &bag))
		assert.Equal(t, map[string]string{
			"user_email": "engineer@example.com",
			"user_name":  "Engineer, Staff",
			"session_id": "session-1",
			"client_app": "claude_code",
		}, bag)
	})

	t.Run("omits empty identity fields", func(t *testing.T) {
		upstream := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
		creds := &proxy.Credentials{IdentityHeader: "X-Caller-Identity", IdentityHeaderFormat: auth.IdentityFormatJSON}
		proxy.ApplyIdentityHeader(identityCtx(creds, proxy.ClientIdentity{Email: "engineer@example.com"}), upstream)

		decoded, err := url.QueryUnescape(upstream.Header.Get("X-Caller-Identity"))
		require.NoError(t, err)
		assert.JSONEq(t, `{"user_email":"engineer@example.com"}`, decoded,
			"an endpoint must not have to tell an absent field from a blank one")
	})

	t.Run("sends nothing when the request carries no identity", func(t *testing.T) {
		upstream := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
		creds := &proxy.Credentials{IdentityHeader: "X-Caller-Identity", IdentityHeaderFormat: auth.IdentityFormatJSON}
		proxy.ApplyIdentityHeader(identityCtx(creds, proxy.ClientIdentity{}), upstream)
		assert.Empty(t, upstream.Header.Get("X-Caller-Identity"),
			"an empty header is worse than none: it attributes the turn to nobody instead of leaving it unattributed")
	})

	t.Run("overrides a client-supplied value of the same name", func(t *testing.T) {
		upstream := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
		upstream.Header.Set("X-Caller-Identity", "someone-else@example.com")
		creds := &proxy.Credentials{IdentityHeader: "X-Caller-Identity", IdentityHeaderFormat: auth.IdentityFormatEmail}
		proxy.ApplyIdentityHeader(identityCtx(creds, identity), upstream)
		assert.Equal(t, "engineer@example.com", upstream.Header.Get("X-Caller-Identity"),
			"a caller must not be able to bill their turns to another user by setting the header themselves")
	})
}
