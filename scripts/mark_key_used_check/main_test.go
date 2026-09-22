// Run against a disposable database with router migrations applied:
//
//	ROUTER_TEST_DATABASE_URL=... go test ./scripts/mark_key_used_check -count=1
package mark_key_used_check_test

import (
	"context"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/postgres"
)

func TestMarkUsedSerializesFirstUseAndKeepsUpdatingLastUse(t *testing.T) {
	dsn := os.Getenv("ROUTER_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("ROUTER_TEST_DATABASE_URL not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	const callers = 16
	repos := make([]auth.APIKeyRepository, callers)
	var root *postgres.Repository
	for i := range repos {
		conn, err := pgx.Connect(ctx, dsn)
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, conn.Close(context.Background())) })
		repo := postgres.NewRepository(conn, auth.NoOpEncryptor{})
		repos[i] = repo.APIKeys
		if i == 0 {
			root = repo
		}
	}
	installation, err := root.Installations.Create(ctx, auth.CreateInstallationParams{
		ExternalID: uuid.NewString(), Name: "first-use-regression",
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, root.Installations.SoftDelete(context.Background(), installation.ExternalID, installation.ID))
	})

	for range 8 {
		keyHash := uuid.NewString()
		key, err := root.APIKeys.Create(ctx, auth.CreateAPIKeyParams{
			InstallationID: installation.ID, ExternalID: uuid.NewString(),
			KeyPrefix: "rk_test", KeyHash: keyHash, KeySuffix: "test", Scope: auth.ScopeRouting,
		})
		require.NoError(t, err)
		t.Cleanup(func() {
			_, err := root.APIKeys.SoftDelete(context.Background(), installation.ID, key.ID)
			require.NoError(t, err)
		})
		start := make(chan struct{})
		first := make([]bool, callers)
		errs := make([]error, callers)
		var workers sync.WaitGroup
		for i := range repos {
			workers.Add(1)
			go func() {
				defer workers.Done()
				<-start
				first[i], errs[i] = repos[i].MarkUsed(ctx, key.ID)
			}()
		}
		close(start)
		workers.Wait()
		count := 0
		for i := range first {
			require.NoError(t, errs[i])
			if first[i] {
				count++
			}
		}
		require.Equal(t, 1, count, "only one concurrent request may report first use")

		before, _, err := root.APIKeys.GetActiveByHashWithInstallation(ctx, keyHash)
		require.NoError(t, err)
		require.NotNil(t, before.LastUsedAt)
		firstUse, err := root.APIKeys.MarkUsed(ctx, key.ID)
		require.NoError(t, err)
		require.False(t, firstUse)
		after, _, err := root.APIKeys.GetActiveByHashWithInstallation(ctx, keyHash)
		require.NoError(t, err)
		require.True(t, after.LastUsedAt.After(*before.LastUsedAt), "repeat use must advance last_used_at")

		_, err = root.APIKeys.SoftDelete(ctx, installation.ID, key.ID)
		require.NoError(t, err)
		firstUse, err = root.APIKeys.MarkUsed(ctx, key.ID)
		require.NoError(t, err)
		require.False(t, firstUse, "deleted keys must not report first use")
	}
	firstUse, err := root.APIKeys.MarkUsed(ctx, uuid.NewString())
	require.NoError(t, err)
	require.False(t, firstUse, "missing keys must not report first use")
}
