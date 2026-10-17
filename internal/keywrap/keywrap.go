// Package keywrap builds the key-encryption key (KEK) that unwraps the
// router's Tink keyset at boot, so the keyset can be stored encrypted rather
// than as cleartext JSON.
//
// Two KEK sources are supported:
//
//   - A HashiCorp Vault or OpenBao transit key, addressed by a Tink key URI
//     of the form hcvault://<host>[:port]/<mount>/keys/<name>. Decryption is
//     a remote call; the KEK never leaves the KMS.
//   - A cleartext Tink AEAD keyset supplied separately from the wrapped
//     keyset, so the two can be kept in different secret stores.
//
// This is an adapter: Vault is reached over the network. Composition happens
// in cmd/router/main.go.
package keywrap

import (
	"bytes"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/hashicorp/vault/api"
	"github.com/tink-crypto/tink-go-hcvault/v2/integration/hcvault"
	"github.com/tink-crypto/tink-go/v2/aead"
	"github.com/tink-crypto/tink-go/v2/insecurecleartextkeyset"
	"github.com/tink-crypto/tink-go/v2/keyset"
	"github.com/tink-crypto/tink-go/v2/tink"
)

const hcvaultScheme = "hcvault"

// Config selects the KEK. At most one of KEKURI and KEKKeysetJSON may be set.
type Config struct {
	// KEKURI is a Tink key URI for a Vault/OpenBao transit key:
	// hcvault://<host>[:port]/<mount>/keys/<name>.
	KEKURI string
	// KEKKeysetJSON is a cleartext Tink AEAD keyset used directly as the KEK.
	KEKKeysetJSON string
	// VaultAddr overrides the server address. Empty means https://<host> from
	// KEKURI. Set it for a plain-HTTP or differently-addressed server.
	VaultAddr string
	// VaultToken authenticates transit decrypt calls. Required with KEKURI.
	VaultToken string
}

// Enabled reports whether any KEK source is configured.
func (c Config) Enabled() bool {
	return strings.TrimSpace(c.KEKURI) != "" || strings.TrimSpace(c.KEKKeysetJSON) != ""
}

// NewKEK returns the AEAD configured by c. Every misconfiguration is an
// error; there is no fallback to a cleartext keyset.
func NewKEK(c Config) (tink.AEAD, error) {
	uri := strings.TrimSpace(c.KEKURI)
	local := strings.TrimSpace(c.KEKKeysetJSON)
	switch {
	case uri != "" && local != "":
		return nil, errors.New("keywrap: set either a KEK URI or a KEK keyset, not both")
	case uri != "":
		return newVaultKEK(uri, c.VaultAddr, c.VaultToken)
	case local != "":
		return newLocalKEK(local)
	default:
		return nil, errors.New("keywrap: no KEK configured")
	}
}

func newLocalKEK(keysetJSON string) (tink.AEAD, error) {
	handle, err := insecurecleartextkeyset.Read(keyset.NewJSONReader(bytes.NewBufferString(keysetJSON)))
	if err != nil {
		return nil, fmt.Errorf("keywrap: read KEK keyset: %w", err)
	}
	primitive, err := aead.New(handle)
	if err != nil {
		return nil, fmt.Errorf("keywrap: KEK keyset is not an AEAD keyset: %w", err)
	}
	return primitive, nil
}

func newVaultKEK(keyURI, addr, token string) (tink.AEAD, error) {
	u, err := url.Parse(keyURI)
	if err != nil {
		return nil, fmt.Errorf("keywrap: parse KEK URI: %w", err)
	}
	if !strings.EqualFold(u.Scheme, hcvaultScheme) {
		return nil, fmt.Errorf("keywrap: KEK URI scheme must be %s://, got %q", hcvaultScheme, u.Scheme)
	}
	if u.Host == "" {
		return nil, errors.New("keywrap: KEK URI has no host")
	}
	if strings.TrimSpace(token) == "" {
		return nil, errors.New("keywrap: a Vault token is required with a KEK URI")
	}
	addr = strings.TrimSpace(addr)
	if addr == "" {
		addr = "https://" + u.Host
	}

	// DefaultConfig reads the standard VAULT_CACERT / VAULT_CLIENT_CERT /
	// VAULT_SKIP_VERIFY TLS settings; the address and token are set explicitly.
	cfg := api.DefaultConfig()
	if cfg.Error != nil {
		return nil, fmt.Errorf("keywrap: vault client config: %w", cfg.Error)
	}
	cfg.Address = addr
	client, err := api.NewClient(cfg)
	if err != nil {
		return nil, fmt.Errorf("keywrap: vault client: %w", err)
	}
	client.SetToken(token)

	kek, err := hcvault.NewAEAD(u.EscapedPath(), client.Logical())
	if err != nil {
		return nil, fmt.Errorf("keywrap: KEK URI path must be /<mount>/keys/<name>: %w", err)
	}
	return kek, nil
}

// Wrap encrypts a cleartext Tink keyset JSON under kek and returns the
// encrypted keyset JSON that auth.NewWrappedTinkEncryptor reads.
func Wrap(cleartextKeysetJSON string, kek tink.AEAD) (string, error) {
	handle, err := insecurecleartextkeyset.Read(keyset.NewJSONReader(bytes.NewBufferString(cleartextKeysetJSON)))
	if err != nil {
		return "", fmt.Errorf("keywrap: read keyset: %w", err)
	}
	var out bytes.Buffer
	if err := handle.Write(keyset.NewJSONWriter(&out), kek); err != nil {
		return "", fmt.Errorf("keywrap: encrypt keyset: %w", err)
	}
	return out.String(), nil
}
