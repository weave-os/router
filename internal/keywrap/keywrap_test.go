package keywrap_test

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tink-crypto/tink-go/v2/aead"
	"github.com/tink-crypto/tink-go/v2/insecurecleartextkeyset"
	"github.com/tink-crypto/tink-go/v2/keyset"
	"github.com/tink-crypto/tink-go/v2/tink"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/keywrap"
)

func newKeysetJSON(t *testing.T) string {
	t.Helper()
	handle, err := keyset.NewHandle(aead.AES256GCMKeyTemplate())
	require.NoError(t, err)
	var buf bytes.Buffer
	require.NoError(t, insecurecleartextkeyset.Write(handle, keyset.NewJSONWriter(&buf)))
	return buf.String()
}

// assertSameKeys proves enc decrypts what a cleartext encryptor over the
// original keyset produced, i.e. the unwrapped keyset is the original one.
func assertSameKeys(t *testing.T, cleartextKeyset string, enc auth.Encryptor) {
	t.Helper()
	original, err := auth.NewTinkEncryptor(cleartextKeyset)
	require.NoError(t, err)
	ciphertext, err := original.Encrypt([]byte("sk-ant-secret"), "org_1", "anthropic")
	require.NoError(t, err)
	got, err := enc.Decrypt(ciphertext, "org_1", "anthropic")
	require.NoError(t, err)
	assert.Equal(t, []byte("sk-ant-secret"), got)
}

func TestLocalKEK_RoundTrip(t *testing.T) {
	kekJSON := newKeysetJSON(t)
	dataKeyset := newKeysetJSON(t)

	kek, err := keywrap.NewKEK(keywrap.Config{KEKKeysetJSON: kekJSON})
	require.NoError(t, err)
	wrapped, err := keywrap.Wrap(dataKeyset, kek)
	require.NoError(t, err)
	assert.Contains(t, wrapped, "encryptedKeyset")
	assert.NotContains(t, wrapped, `"value"`, "wrapped keyset must not carry cleartext key material")

	enc, err := auth.NewWrappedTinkEncryptor(wrapped, kek)
	require.NoError(t, err)
	assertSameKeys(t, dataKeyset, enc)
}

func TestLocalKEK_WrongKEKFails(t *testing.T) {
	right, err := keywrap.NewKEK(keywrap.Config{KEKKeysetJSON: newKeysetJSON(t)})
	require.NoError(t, err)
	wrong, err := keywrap.NewKEK(keywrap.Config{KEKKeysetJSON: newKeysetJSON(t)})
	require.NoError(t, err)

	wrapped, err := keywrap.Wrap(newKeysetJSON(t), right)
	require.NoError(t, err)
	_, err = auth.NewWrappedTinkEncryptor(wrapped, wrong)
	require.Error(t, err)
}

func TestWrappedEncryptor_RejectsCleartextKeyset(t *testing.T) {
	kek, err := keywrap.NewKEK(keywrap.Config{KEKKeysetJSON: newKeysetJSON(t)})
	require.NoError(t, err)
	_, err = auth.NewWrappedTinkEncryptor(newKeysetJSON(t), kek)
	require.Error(t, err, "a cleartext keyset must not load when a KEK is configured")
}

func TestNewKEK_ConfigErrors(t *testing.T) {
	cases := []struct {
		name string
		cfg  keywrap.Config
		want string
	}{
		{"nothing set", keywrap.Config{}, "no KEK configured"},
		{"both set", keywrap.Config{KEKURI: "hcvault://vault/transit/keys/k", KEKKeysetJSON: "{}", VaultToken: "t"}, "not both"},
		{"wrong scheme", keywrap.Config{KEKURI: "gcp-kms://projects/p/locations/l/keyRings/r/cryptoKeys/k", VaultToken: "t"}, "scheme"},
		{"no host", keywrap.Config{KEKURI: "hcvault:///transit/keys/k", VaultToken: "t"}, "no host"},
		{"no token", keywrap.Config{KEKURI: "hcvault://vault/transit/keys/k"}, "token"},
		{"bad path", keywrap.Config{KEKURI: "hcvault://vault/transit/k", VaultToken: "t"}, "/<mount>/keys/<name>"},
		{"malformed keyset", keywrap.Config{KEKKeysetJSON: "not json"}, "read KEK keyset"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := keywrap.NewKEK(tc.cfg)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
		})
	}
	assert.False(t, keywrap.Config{}.Enabled())
	assert.True(t, keywrap.Config{KEKURI: "hcvault://v/t/keys/k"}.Enabled())
}

// fakeTransit implements the two Vault transit endpoints Tink calls, backed
// by a local AEAD, and requires a fixed token.
type fakeTransit struct {
	t     *testing.T
	token string
	mount string
	key   string
	aead  tink.AEAD
	calls int
}

func (f *fakeTransit) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.calls++
	if r.Header.Get("X-Vault-Token") != f.token {
		http.Error(w, `{"errors":["permission denied"]}`, http.StatusForbidden)
		return
	}
	var body map[string]string
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, `{"errors":["bad body"]}`, http.StatusBadRequest)
		return
	}
	ad, _ := base64.StdEncoding.DecodeString(body["associated_data"])
	var data map[string]string
	switch r.URL.Path {
	case "/v1/" + f.mount + "/encrypt/" + f.key:
		pt, err := base64.StdEncoding.DecodeString(body["plaintext"])
		require.NoError(f.t, err)
		ct, err := f.aead.Encrypt(pt, ad)
		require.NoError(f.t, err)
		data = map[string]string{"ciphertext": "vault:v1:" + base64.StdEncoding.EncodeToString(ct)}
	case "/v1/" + f.mount + "/decrypt/" + f.key:
		raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(body["ciphertext"], "vault:v1:"))
		if err != nil {
			http.Error(w, `{"errors":["invalid ciphertext"]}`, http.StatusBadRequest)
			return
		}
		pt, err := f.aead.Decrypt(raw, ad)
		if err != nil {
			http.Error(w, `{"errors":["cipher: message authentication failed"]}`, http.StatusBadRequest)
			return
		}
		data = map[string]string{"plaintext": base64.StdEncoding.EncodeToString(pt)}
	default:
		http.Error(w, `{"errors":["no handler"]}`, http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	require.NoError(f.t, json.NewEncoder(w).Encode(map[string]any{"data": data}))
}

func newFakeTransit(t *testing.T) (*fakeTransit, *httptest.Server) {
	t.Helper()
	handle, err := keyset.NewHandle(aead.AES256GCMKeyTemplate())
	require.NoError(t, err)
	primitive, err := aead.New(handle)
	require.NoError(t, err)
	f := &fakeTransit{t: t, token: "s.test-token", mount: "transit", key: "router-kek", aead: primitive}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	return f, srv
}

func TestVaultKEK_RoundTrip(t *testing.T) {
	f, srv := newFakeTransit(t)
	cfg := keywrap.Config{
		KEKURI:     "hcvault://" + srv.Listener.Addr().String() + "/transit/keys/router-kek",
		VaultAddr:  srv.URL,
		VaultToken: f.token,
	}
	kek, err := keywrap.NewKEK(cfg)
	require.NoError(t, err)

	dataKeyset := newKeysetJSON(t)
	wrapped, err := keywrap.Wrap(dataKeyset, kek)
	require.NoError(t, err)

	// A fresh KEK from the same config, as the router builds at boot.
	bootKEK, err := keywrap.NewKEK(cfg)
	require.NoError(t, err)
	enc, err := auth.NewWrappedTinkEncryptor(wrapped, bootKEK)
	require.NoError(t, err)
	assertSameKeys(t, dataKeyset, enc)
	assert.Equal(t, 2, f.calls, "one transit encrypt and one transit decrypt")
}

func TestVaultKEK_WrongTokenFails(t *testing.T) {
	f, srv := newFakeTransit(t)
	uri := "hcvault://" + srv.Listener.Addr().String() + "/transit/keys/router-kek"
	kek, err := keywrap.NewKEK(keywrap.Config{KEKURI: uri, VaultAddr: srv.URL, VaultToken: f.token})
	require.NoError(t, err)
	wrapped, err := keywrap.Wrap(newKeysetJSON(t), kek)
	require.NoError(t, err)

	denied, err := keywrap.NewKEK(keywrap.Config{KEKURI: uri, VaultAddr: srv.URL, VaultToken: "s.wrong"})
	require.NoError(t, err)
	_, err = auth.NewWrappedTinkEncryptor(wrapped, denied)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "permission denied")
}

func TestVaultKEK_WrongKeyFails(t *testing.T) {
	f, srv := newFakeTransit(t)
	addr := srv.Listener.Addr().String()
	kek, err := keywrap.NewKEK(keywrap.Config{KEKURI: "hcvault://" + addr + "/transit/keys/router-kek", VaultAddr: srv.URL, VaultToken: f.token})
	require.NoError(t, err)
	wrapped, err := keywrap.Wrap(newKeysetJSON(t), kek)
	require.NoError(t, err)

	other, err := keywrap.NewKEK(keywrap.Config{KEKURI: "hcvault://" + addr + "/transit/keys/other", VaultAddr: srv.URL, VaultToken: f.token})
	require.NoError(t, err)
	_, err = auth.NewWrappedTinkEncryptor(wrapped, other)
	require.Error(t, err)
}
