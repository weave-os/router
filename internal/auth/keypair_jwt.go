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

const minKeypairKeyBits = 2048

const KeypairTokenTTL = 55 * time.Minute

const maxKeypairTTL = time.Hour - keypairClockSkew

const keypairClockSkew = 30 * time.Second

func ParseKeypairPrivateKey(pemBytes []byte) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return nil, fmt.Errorf("%w: private key is not PEM encoded", ErrInvalidKeypairAuth)
	}
	if _, legacyEncrypted := block.Headers["DEK-Info"]; legacyEncrypted || strings.Contains(block.Type, "ENCRYPTED") {
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

func PublicKeyFingerprint(pub *rsa.PublicKey) (string, error) {
	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		return "", fmt.Errorf("%w: cannot encode public key", ErrInvalidKeypairAuth)
	}
	sum := sha256.Sum256(der)
	return "SHA256:" + base64.StdEncoding.EncodeToString(sum[:]), nil
}

func MintKeypairJWT(key *rsa.PrivateKey, account, user string, now time.Time, ttl time.Duration) (string, error) {
	fingerprint, err := PublicKeyFingerprint(&key.PublicKey)
	if err != nil {
		return "", err
	}
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
