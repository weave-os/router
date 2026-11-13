package postgres

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestInt64PtrFromUSD_PreservesNegativeEV(t *testing.T) {
	neg := -0.012345
	got := int64PtrFromUSD(&neg)
	require.NotNil(t, got)
	assert.Equal(t, int64(-12345), *got)
}

func TestInt64PtrFromUSD_NilStaysNil(t *testing.T) {
	assert.Nil(t, int64PtrFromUSD(nil))
}

func TestInt64PtrFromUSD_PositiveRounds(t *testing.T) {
	pos := 0.01
	got := int64PtrFromUSD(&pos)
	require.NotNil(t, got)
	assert.Equal(t, int64(10000), *got)
}
