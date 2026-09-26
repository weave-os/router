package compactioncheckpoint

import (
	"context"
	"time"
)

type Checkpoint struct {
	CredentialIdentity string
	SessionKey         [16]byte
	Endpoint           string
	PrefixDigest       [32]byte
	PolicyDigest       [32]byte
	Boundary           int
	Summary            string
	Model              string
	ExpiresAt          time.Time
}

type Store interface {
	Get(ctx context.Context, credentialIdentity string, sessionKey [16]byte, endpoint string) (Checkpoint, bool, error)
	Upsert(ctx context.Context, checkpoint Checkpoint) error
}
