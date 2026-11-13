package admin

import (
	"testing"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/proxy"

	"github.com/stretchr/testify/assert"
)

func TestContentCaptureFor(t *testing.T) {
	mode := func(s string) *string { return &s }

	t.Run("no override reads back the deployment mode", func(t *testing.T) {
		got := contentCaptureFor(&auth.Installation{}, proxy.CaptureFull)
		assert.Equal(t, "full", got.Deployment)
		assert.Nil(t, got.Installation)
		assert.Equal(t, "full", got.Effective)
	})

	t.Run("override tightens the effective mode", func(t *testing.T) {
		got := contentCaptureFor(&auth.Installation{ContentCaptureMode: mode("off")}, proxy.CaptureFull)
		assert.Equal(t, "off", got.Effective)
	})

	t.Run("override cannot widen past the deployment mode", func(t *testing.T) {
		got := contentCaptureFor(&auth.Installation{ContentCaptureMode: mode("full")}, proxy.CaptureHashed)
		assert.Equal(t, "hashed", got.Effective)
		assert.Equal(t, "full", *got.Installation,
			"the stored request stays visible even when the deployment caps it")
	})
}
