package auth

import (
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// minKeypairKeyBits rejects RSA keys too small to be worth signing with.
const minKeypairKeyBits = 2048

// KeypairTokenTTL is how long a minted token claims validity. Snowflake clamps
// key-pair JWTs to one hour regardless of exp, so this stays under that ceiling.
const KeypairTokenTTL = 55 * time.Minute

// maxKeypairTTL is the longest validity a minted token may claim; the skew
// backdate keeps the signed window itself under the upstream's one-hour cap.
const maxKeypairTTL = time.Hour - keypairClockSkew

// keypairClockSkew backdates iat so a signer running slightly ahead of the
// upstream clock does not produce a token from the future.
const keypairClockSkew = 30 * time.Second

// ParseKeypairPrivateKey decodes a PEM-encoded RSA private key (PKCS#1 or PKCS#8).
// Passphrase-protected keys are rejected: there is nobody to prompt for the passphrase.
func ParseKeypairPrivateKey(pemBytes []byte) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return nil, fmt.Errorf("%w: private key is not PEM encoded", ErrInvalidKeypairAuth)
	}
	//nolint:staticcheck // x509.IsEncryptedPEMBlock is the only way to detect the legacy encrypted form we reject.
	if x509.IsEncryptedPEMBlock(block) || strings.Contains(block.Type, "ENCRYPTED") {
		return nil, fmt.Errorf("%w: passphrase-protected private keys are not supported", ErrInvalidKeypairAuth)
	}
	key, err := parseRSAPrivateKey(block)
	if err != nil {
		return nil, err
	}
	if key.N.BitLen() < minKeypairKeyBits {
		return nil, fmt.Errorf("%w: RSA key must be at least %d bits", ErrInvalidKeypairAuth, minKeypairKeyBits)
	}
	return key, nil
}

// parseRSAPrivateKey reads a decoded PEM block as PKCS#1 or PKCS#8 RSA.
func parseRSAPrivateKey(block *pem.Block) (*rsa.PrivateKey, error) {
	if key, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return key, nil
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("%w: private key is neither PKCS#1 nor PKCS#8 RSA", ErrInvalidKeypairAuth)
	}
	key, ok := parsed.(*rsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("%w: private key is not RSA", ErrInvalidKeypairAuth)
	}
	return key, nil
}

// PublicKeyFingerprint renders the SHA256:<base64> fingerprint of pub's DER
// SPKI encoding — the value the upstream reports for the assigned public key.
func PublicKeyFingerprint(pub *rsa.PublicKey) (string, error) {
	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		return "", fmt.Errorf("%w: cannot encode public key", ErrInvalidKeypairAuth)
	}
	sum := sha256.Sum256(der)
	return "SHA256:" + base64.StdEncoding.EncodeToString(sum[:]), nil
}

// MintKeypairJWT signs a key-pair JWT for account/user, valid from now for ttl.
// The issuer binds the public-key fingerprint so a rotated key stops validating
// immediately rather than at expiry.
func MintKeypairJWT(key *rsa.PrivateKey, account, user string, now time.Time, ttl time.Duration) (string, error) {
	fingerprint, err := PublicKeyFingerprint(&key.PublicKey)
	if err != nil {
		return "", err
	}
	// Upstreams reject a token claiming more than an hour of validity, so a
	// longer request is honored as the ceiling rather than failing at signing.
	if ttl > maxKeypairTTL {
		ttl = maxKeypairTTL
	}
	subject := strings.ToUpper(account) + "." + strings.ToUpper(user)
	issuedAt := now.Add(-keypairClockSkew)
	token := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.RegisteredClaims{
		Issuer:    subject + "." + fingerprint,
		Subject:   subject,
		IssuedAt:  jwt.NewNumericDate(issuedAt),
		ExpiresAt: jwt.NewNumericDate(now.Add(ttl)),
	})
	signed, err := token.SignedString(key)
	if err != nil {
		return "", fmt.Errorf("sign keypair jwt: %w", err)
	}
	return signed, nil
}
