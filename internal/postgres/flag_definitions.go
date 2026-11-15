package postgres

import (
	"context"
	"fmt"

	"weave-os/router/internal/flags"
	"weave-os/router/internal/postgres/dbbudget"
	"weave-os/router/internal/sqlc"
)

type FlagDefinitionRepo struct {
	tx sqlc.DBTX
}

func NewFlagDefinitionRepo(tx sqlc.DBTX) *FlagDefinitionRepo {
	return &FlagDefinitionRepo{tx: tx}
}

func (r *FlagDefinitionRepo) Publish(ctx context.Context, defs []flags.PublishedDefinition) error {
	q := dbbudget.Queries(r.tx)
	keys := make([]string, 0, len(defs))
	for _, def := range defs {
		defaultValue := def.DeploymentDefault
		err := q.UpsertFlagDefinition(ctx, sqlc.UpsertFlagDefinitionParams{
			Key:               string(def.Key),
			Kind:              string(def.Kind),
			EnvVar:            def.EnvVar,
			DeploymentDefault: defaultValue,
			OrgOverridable:    def.OrgOverridable,
			Description:       def.Description,
			RegistryVersion:   flags.RegistryVersion,
		})
		if err != nil {
			return fmt.Errorf("upsert flag definition %q: %w", def.Key, err)
		}
		keys = append(keys, string(def.Key))
	}
	err := q.DeleteFlagDefinitionsNotIn(ctx, sqlc.DeleteFlagDefinitionsNotInParams{
		Keys:            keys,
		RegistryVersion: flags.RegistryVersion,
	})
	if err != nil {
		return fmt.Errorf("prune retired flag definitions: %w", err)
	}
	return nil
}

var _ flags.DefinitionRepository = (*FlagDefinitionRepo)(nil)
