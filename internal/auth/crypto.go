package auth

import (
	"bytes"
	"fmt"

	"github.com/tink-crypto/tink-go/v2/aead"
	"github.com/tink-crypto/tink-go/v2/insecurecleartextkeyset"
	"github.com/tink-crypto/tink-go/v2/keyset"
	"github.com/tink-crypto/tink-go/v2/tink"
)

type Encryptor interface {
	Encrypt(plaintext []byte, externalID, provider string) (ciphertext []byte, err error)
	Decrypt(ciphertext []byte, externalID, provider string) (plaintext []byte, err error)
}

type tinkEncryptor struct {
	aead tink.AEAD
}

func NewTinkEncryptor(keysetJSON string) (Encryptor, error) {
	reader := keyset.NewJSONReader(bytes.NewBufferString(keysetJSON))
	handle, err := insecurecleartextkeyset.Read(reader)
	if err != nil {
		return nil, fmt.Errorf("read keyset: %w", err)
	}
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

func aadFor(externalID, provider string) []byte {
	return []byte(externalID + "\x00" + provider)
}

type NoOpEncryptor struct{}

func (NoOpEncryptor) Encrypt(plaintext []byte, _, _ string) ([]byte, error) {
	return plaintext, nil
}

func (NoOpEncryptor) Decrypt(ciphertext []byte, _, _ string) ([]byte, error) {
	return ciphertext, nil
}
