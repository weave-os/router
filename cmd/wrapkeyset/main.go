// Command wrapkeyset encrypts a cleartext Tink keyset under a key-encryption
// key, producing the value for EXTERNAL_KEY_ENCRYPTION_KEY when
// EXTERNAL_KEY_ENCRYPTION_KEK_URI or EXTERNAL_KEY_ENCRYPTION_KEK is set.
//
// It reads the cleartext keyset JSON on stdin and writes the encrypted keyset
// JSON on stdout. The KEK is configured with the same environment variables
// the router reads at boot.
//
//	tinkey create-keyset --key-template AES256_GCM --out-format json \
//	  | EXTERNAL_KEY_ENCRYPTION_KEK_URI=hcvault://vault:8200/transit/keys/router \
//	    VAULT_TOKEN=... go run ./cmd/wrapkeyset
package main

import (
	"fmt"
	"io"
	"os"

	"weave-os/router/internal/keywrap"
)

func main() {
	if err := run(os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "wrapkeyset:", err)
		os.Exit(1)
	}
}

func run(in io.Reader, out io.Writer) error {
	cfg := keywrap.Config{
		KEKURI:        os.Getenv("EXTERNAL_KEY_ENCRYPTION_KEK_URI"),
		KEKKeysetJSON: os.Getenv("EXTERNAL_KEY_ENCRYPTION_KEK"),
		VaultAddr:     os.Getenv("VAULT_ADDR"),
		VaultToken:    os.Getenv("VAULT_TOKEN"),
	}
	kek, err := keywrap.NewKEK(cfg)
	if err != nil {
		return err
	}
	cleartext, err := io.ReadAll(in)
	if err != nil {
		return fmt.Errorf("read stdin: %w", err)
	}
	wrapped, err := keywrap.Wrap(string(cleartext), kek)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(out, wrapped)
	return err
}
