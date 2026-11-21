package hmm_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"weave-os/router/internal/router/hmm"
)

func TestClassifierTelemetryOfNilMetadataIsEmpty(t *testing.T) {
	assert.Equal(t, hmm.ClassifierTelemetry{}, hmm.ClassifierTelemetryOf(nil))
}
