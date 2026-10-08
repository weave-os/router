package auth

import (
	"bytes"
	"errors"
	"fmt"

	"github.com/tink-crypto/tink-go/v2/aead"
	"github.com/tink-crypto/tink-go/v2/insecurecleartextkeyset"
	"github.com/tink-crypto/tink-go/v2/keyset"
	"github.com/tink-crypto/tink-go/v2/tink"
)

// Encryptor encrypts/decrypts secrets via AES-256-GCM.
// AAD binds each ciphertext to (externalID, purpose). Existing external API
// keys and refresh tokens use the provider as their purpose; subscription
// access tokens use a distinct purpose suffix.
type Encryptor interface {
	Encrypt(plaintext []byte, externalID, provider string) (ciphertext []byte, err error)
	Decrypt(ciphertext []byte, externalID, provider string) (plaintext []byte, err error)
}

type tinkEncryptor struct {
	aead tink.AEAD
}

// NewTinkEncryptor creates an Encryptor from a cleartext Tink keyset JSON.
func NewTinkEncryptor(keysetJSON string) (Encryptor, error) {
	reader := keyset.NewJSONReader(bytes.NewBufferString(keysetJSON))
	handle, err := insecurecleartextkeyset.Read(reader)
	if err != nil {
		return nil, fmt.Errorf("read keyset: %w", err)
	}
	return newTinkEncryptorFromHandle(handle)
}

// NewWrappedTinkEncryptor creates an Encryptor from a Tink keyset JSON whose
// key material is encrypted under kek, a key-encryption key that usually
// lives in a KMS. The keyset is decrypted once, here; kek is not retained.
func NewWrappedTinkEncryptor(encryptedKeysetJSON string, kek tink.AEAD) (Encryptor, error) {
	if kek == nil {
		return nil, errors.New("read encrypted keyset: nil key-encryption key")
	}
	reader := keyset.NewJSONReader(bytes.NewBufferString(encryptedKeysetJSON))
	handle, err := keyset.Read(reader, kek)
	if err != nil {
		return nil, fmt.Errorf("read encrypted keyset: %w", err)
	}
	return newTinkEncryptorFromHandle(handle)
}

func newTinkEncryptorFromHandle(handle *keyset.Handle) (Encryptor, error) {
	primitive, err := aead.New(handle)
	if err != nil {
		return nil, fmt.Errorf("create AEAD primitive: %w", err)
	}
	return &tinkEncryptor{aead: primitive}, nil
}

func (e *tinkEncryptor) Encrypt(plaintext []byte, externalID, provider string) ([]byte, error) {
	return e.aead.Encrypt(plaintext, aadFor(externalID, provider))
}

func (e *tinkEncryptor) Decrypt(ciphertext []byte, externalID, provider string) ([]byte, error) {
	return e.aead.Decrypt(ciphertext, aadFor(externalID, provider))
}

// aadFor MUST stay byte-identical with the Weave-side helper at
// backend/internal/app/weaverouter/crypto.go.
func aadFor(externalID, provider string) []byte {
	return []byte(externalID + "\x00" + provider)
}

// NoOpEncryptor is a pass-through for development without encryption.
type NoOpEncryptor struct{}

func (NoOpEncryptor) Encrypt(plaintext []byte, _, _ string) ([]byte, error) {
	return plaintext, nil
}

func (NoOpEncryptor) Decrypt(ciphertext []byte, _, _ string) ([]byte, error) {
	return ciphertext, nil
}
