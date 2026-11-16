package otel

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestGenerateTraceID_SucceedsWithRealCryptoRand(t *testing.T) {
	id := generateTraceID()
	assert.Len(t, id, 16)
}

func TestGenerateSpanID_SucceedsWithRealCryptoRand(t *testing.T) {
	id := generateSpanID()
	assert.Len(t, id, 8)
}
