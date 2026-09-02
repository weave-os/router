package main

import (
	"context"
	"testing"
	"time"

	"workweave/router/internal/providers"
	"workweave/router/internal/router/modelstatus"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestInitializeModelStatusesUsesDeploymentWiring(t *testing.T) {
	store := modelstatus.New(time.Now, time.Minute, 5*time.Minute, nil)
	online, offline := initializeModelStatuses(context.Background(), store, map[string]struct{}{providers.ProviderAnthropic: {}})

	require.Positive(t, online)
	require.Positive(t, offline)
	assert.Equal(t, modelstatus.StatusOnline, store.Lookup(context.Background(), modelstatus.Key{ModelID: "claude-sonnet-4-5", Provider: providers.ProviderAnthropic}))
	assert.Equal(t, modelstatus.StatusOffline, store.Lookup(context.Background(), modelstatus.Key{ModelID: "gpt-4.1", Provider: providers.ProviderOpenAI}))
}
