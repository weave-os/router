package proxy

import (
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
)

type progressArmingWriter struct {
	*httptest.ResponseRecorder
	armed bool
}

func (w *progressArmingWriter) ArmOutputProgress(func()) bool {
	w.armed = true
	return true
}

// The Anthropic client arms its idle and stall watchdogs through the response
// writer chain; an observer in that chain must not disarm them.
func TestPassiveRefusalObserver_ForwardsOutputProgressArming(t *testing.T) {
	inner := &progressArmingWriter{ResponseRecorder: httptest.NewRecorder()}

	assert.True(t, newPassiveRefusalObserver(inner).ArmOutputProgress(func() {}))
	assert.True(t, inner.armed)
	assert.False(t, newPassiveRefusalObserver(httptest.NewRecorder()).ArmOutputProgress(func() {}))
}
