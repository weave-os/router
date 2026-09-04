package postgres

import (
	"context"
	"fmt"

	"weave-os/router/internal/flags"
	"weave-os/router/internal/sqlc"
)

// FlagDefinitionRepo publishes the compiled-in flag registry to
// router.flag_definitions. Write-only from the router's perspective: nothing in
// the request path reads the table, so a failed publish degrades the control
// plane's admin UI without touching routing.
type FlagDefinitionRepo struct {
	tx sqlc.DBTX
}

func NewFlagDefinitionRepo(tx sqlc.DBTX) *FlagDefinitionRepo {
	return &FlagDefinitionRepo{tx: tx}
}

// Publish upserts each definition and prunes rows for retired flags.
//
// Not run in a transaction: every replica publishes the same rows at boot, so a
// partial write is corrected by the next boot (or by a peer replica finishing its
// own pass), and holding a transaction open across the whole registry would
// serialize concurrent replica startups for no benefit. RegistryVersion makes
// the prune safe during rolling deploys: an older revision cannot delete a row
// written by a newer revision.
func (r *FlagDefinitionRepo) Publish(ctx context.Context, defs []flags.PublishedDefinition) error {
	q := sqlc.New(r.tx)
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

// Keep the repository's generated DB contract and the registry in sync at
// compile time.
var _ flags.DefinitionRepository = (*FlagDefinitionRepo)(nil)
