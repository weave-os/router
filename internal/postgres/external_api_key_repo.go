package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/sqlc"

	"github.com/google/uuid"
)

// ExternalAPIKeyRepo implements auth.ExternalAPIKeyRepository over SQLC.
type ExternalAPIKeyRepo struct {
	tx        sqlc.DBTX
	encryptor auth.Encryptor
}

// NewExternalAPIKeyRepo constructs an ExternalAPIKeyRepo.
func NewExternalAPIKeyRepo(tx sqlc.DBTX, encryptor auth.Encryptor) *ExternalAPIKeyRepo {
	return &ExternalAPIKeyRepo{tx: tx, encryptor: encryptor}
}

func (r *ExternalAPIKeyRepo) Create(ctx context.Context, params auth.CreateExternalAPIKeyParams) (*auth.ExternalAPIKey, error) {
	installationUUID, err := uuid.Parse(params.InstallationID)
	if err != nil {
		return nil, err
	}

	aliases, err := marshalModelAliases(params.ModelAliases)
	if err != nil {
		return nil, err
	}

	q := sqlc.New(r.tx)
	row, err := q.CreateExternalAPIKey(ctx, sqlc.CreateExternalAPIKeyParams{
		InstallationID: installationUUID,
		ExternalID:     params.ExternalID,
		Provider:       params.Provider,
		KeyCiphertext:  params.KeyCiphertext,
		KeyPrefix:      params.KeyPrefix,
		KeySuffix:      params.KeySuffix,
		KeyFingerprint: params.KeyFingerprint,
		Name:           params.Name,
		BaseURL:        params.BaseURL,
		ModelAliases:   aliases,

		IdentityHeaderName:     params.IdentityHeader,
		IdentityHeaderFormat:   params.IdentityHeaderFormat,
		ForwardedClientHeaders: params.ForwardedClientHeaders,
		BaggageHeader:          params.BaggageHeader,

		AuthType:    params.AuthType,
		AuthAccount: params.AuthAccount,
		AuthUser:    params.AuthUser,
		CreatedBy:   params.CreatedBy,
	})
	if err != nil {
		return nil, err
	}

	return toExternalAPIKey(row)
}

func (r *ExternalAPIKeyRepo) GetForInstallation(ctx context.Context, installationID string) ([]*auth.ExternalAPIKey, error) {
	installationUUID, err := uuid.Parse(installationID)
	if err != nil {
		return nil, err
	}

	q := sqlc.New(r.tx)
	rows, err := q.GetActiveExternalAPIKeysForInstallation(ctx, installationUUID)
	if err != nil {
		return nil, err
	}

	keys := make([]*auth.ExternalAPIKey, 0, len(rows))
	for _, row := range rows {
		key, err := toExternalAPIKey(row)
		if err != nil {
			return nil, err
		}
		// Workload-identity rows store no secret at all — the credential is
		// minted per request — so there is nothing to decrypt.
		if len(row.KeyCiphertext) > 0 {
			plaintext, err := r.encryptor.Decrypt(row.KeyCiphertext, row.ExternalID, row.Provider)
			if err != nil {
				return nil, err
			}
			key.Plaintext = plaintext
		}
		keys = append(keys, key)
	}
	return keys, nil
}

func (r *ExternalAPIKeyRepo) SoftDeleteByProvider(ctx context.Context, installationID, provider string) error {
	installationUUID, err := uuid.Parse(installationID)
	if err != nil {
		return err
	}
	q := sqlc.New(r.tx)
	return q.SoftDeleteExternalAPIKeyByProvider(ctx, sqlc.SoftDeleteExternalAPIKeyByProviderParams{
		InstallationID: installationUUID,
		Provider:       provider,
	})
}

func (r *ExternalAPIKeyRepo) UpdateModelAliases(ctx context.Context, installationID, id string, aliases map[string]string) (*auth.ExternalAPIKey, error) {
	installationUUID, err := uuid.Parse(installationID)
	if err != nil {
		return nil, err
	}
	keyUUID, err := uuid.Parse(id)
	if err != nil {
		return nil, err
	}
	encoded, err := marshalModelAliases(aliases)
	if err != nil {
		return nil, err
	}
	q := sqlc.New(r.tx)
	row, err := q.UpdateExternalAPIKeyModelAliases(ctx, sqlc.UpdateExternalAPIKeyModelAliasesParams{
		ID:             keyUUID,
		InstallationID: installationUUID,
		ModelAliases:   encoded,
	})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, auth.ErrExternalAPIKeyNotFound
		}
		return nil, err
	}
	return toExternalAPIKey(row)
}

func (r *ExternalAPIKeyRepo) SoftDelete(ctx context.Context, installationID, id string) error {
	installationUUID, err := uuid.Parse(installationID)
	if err != nil {
		return err
	}
	keyUUID, err := uuid.Parse(id)
	if err != nil {
		return err
	}
	q := sqlc.New(r.tx)
	return q.SoftDeleteExternalAPIKey(ctx, sqlc.SoftDeleteExternalAPIKeyParams{
		ID:             keyUUID,
		InstallationID: installationUUID,
	})
}

func (r *ExternalAPIKeyRepo) MarkUsed(ctx context.Context, id string) error {
	keyUUID, err := uuid.Parse(id)
	if err != nil {
		return err
	}
	q := sqlc.New(r.tx)
	return q.MarkExternalAPIKeyUsed(ctx, keyUUID)
}

// marshalModelAliases encodes the alias map for the jsonb column; a nil or
// empty map stores SQL NULL so "no aliases" has one on-disk representation.
func marshalModelAliases(aliases map[string]string) ([]byte, error) {
	if len(aliases) == 0 {
		return nil, nil
	}
	encoded, err := json.Marshal(aliases)
	if err != nil {
		return nil, fmt.Errorf("encode model aliases: %w", err)
	}
	return encoded, nil
}

func toExternalAPIKey(row sqlc.RouterModelRouterExternalAPIKey) (*auth.ExternalAPIKey, error) {
	key := &auth.ExternalAPIKey{
		ID:             row.ID.String(),
		InstallationID: row.InstallationID.String(),
		Provider:       row.Provider,
		KeyPrefix:      row.KeyPrefix,
		KeySuffix:      row.KeySuffix,
		KeyFingerprint: row.KeyFingerprint,
		BaseURL:        derefString(row.BaseURL),

		IdentityHeader:         derefString(row.IdentityHeaderName),
		IdentityHeaderFormat:   derefString(row.IdentityHeaderFormat),
		ForwardedClientHeaders: row.ForwardedClientHeaders,
		BaggageHeader:          derefString(row.BaggageHeader),

		AuthType:    row.AuthType,
		AuthAccount: derefString(row.AuthAccount),
		AuthUser:    derefString(row.AuthUser),
		CreatedAt:   timestampOrZero(row.CreatedAt),
	}
	key.Name = row.Name
	key.LastUsedAt = timestampPtr(row.LastUsedAt)
	if len(row.ModelAliases) > 0 {
		if err := json.Unmarshal(row.ModelAliases, &key.ModelAliases); err != nil {
			return nil, fmt.Errorf("decode model aliases for key %s: %w", key.ID, err)
		}
	}
	return key, nil
}
