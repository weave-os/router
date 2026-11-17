package auth

import (
	"context"
	"time"
)

type UserIDContextKey struct{}

type UserClusterModelListsContextKey struct{}

func UserIDFrom(ctx context.Context) string {
	s, _ := ctx.Value(UserIDContextKey{}).(string)
	return s
}

func UserClusterModelListsFrom(ctx context.Context) map[string][]string {
	m, _ := ctx.Value(UserClusterModelListsContextKey{}).(map[string][]string)
	return m
}

type User struct {
	ID                string
	InstallationID    string
	Email             string
	ClaudeAccountUUID *string
	DisplayName       *string
	FirstSeenAt       time.Time
	LastSeenAt        time.Time
	DeletedAt         *time.Time
}

type UpsertUserParams struct {
	InstallationID    string
	Email             string
	ClaudeAccountUUID *string
	DisplayName       *string
}

type UpsertUserByAccountUUIDParams struct {
	InstallationID    string
	ClaudeAccountUUID string
	DisplayName       *string
}

type UpsertUserByCredentialSubjectParams struct {
	InstallationID      string
	CredentialSubjectID string
	DisplayName         *string
}

type UserRepository interface {
	UpsertByEmail(ctx context.Context, params UpsertUserParams) (*User, error)
	UpsertByAccountUUID(ctx context.Context, params UpsertUserByAccountUUIDParams) (*User, error)
	UpsertByCredentialSubject(ctx context.Context, params UpsertUserByCredentialSubjectParams) (*User, error)
	Get(ctx context.Context, id string) (*User, error)
	ListForInstallation(ctx context.Context, installationID string) ([]*User, error)
}
