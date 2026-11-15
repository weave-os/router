package otel_test

import (
	"net/http/httptest"
	"testing"

	"weave-os/router/internal/observability/otel"

	"github.com/stretchr/testify/assert"
)

type outputProgressArmer struct {
	*httptest.ResponseRecorder
	armed func()
}

func (a *outputProgressArmer) ArmOutputProgress(mark func()) bool {
	a.armed = mark
	return true
}

func TestUsageExtractor_ForwardsOutputProgressArmingToInnerWriter(t *testing.T) {
	inner := &outputProgressArmer{ResponseRecorder: httptest.NewRecorder()}
	marked := false

	armed := otel.NewUsageExtractor(inner, "anthropic").ArmOutputProgress(func() { marked = true })

	assert.True(t, armed)
	if assert.NotNil(t, inner.armed) {
		inner.armed()
	}
	assert.True(t, marked)
}

func TestUsageExtractor_ReportsUnarmedWhenInnerWriterCannotArm(t *testing.T) {
	assert.False(t, otel.NewUsageExtractor(httptest.NewRecorder(), "anthropic").ArmOutputProgress(func() {}))
}
