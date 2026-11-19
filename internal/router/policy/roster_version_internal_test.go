package policy

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestServedRosterVersionPrecedence(t *testing.T) {
	const (
		sidecar    = "sidecar-roster"
		pinned     = "pinned-served-roster"
		configured = "configured-selection-policy"
	)
	tests := []struct {
		name              string
		configured        string
		pinned            bool
		wantRosterVersion string
	}{
		{name: "sidecar version when no selection policy is configured", wantRosterVersion: sidecar},
		{name: "configured selection policy replaces the sidecar version", configured: configured, wantRosterVersion: configured},
		{name: "a pinned turn reports the roster it was served from", configured: configured, pinned: true, wantRosterVersion: pinned},
		{name: "a pinned turn without configured policy still reports the served roster", pinned: true, wantRosterVersion: pinned},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			assert.Equal(t, test.wantRosterVersion, servedRosterVersion(sidecar, pinned, test.configured, test.pinned))
		})
	}
}
