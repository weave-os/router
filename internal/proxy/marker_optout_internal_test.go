package proxy

import (
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestSuppressMarkerIfRequested pins the opt-out decision: the routing marker is
// dropped only for the recognized disable values, and preserved otherwise.
func TestSuppressMarkerIfRequested(t *testing.T) {
	const marker = "✦ **Weave Router** → deepseek/deepseek-v4-flash · best pick for this turn\n\n"
	cases := []struct {
		name      string
		setHeader bool
		value     string
		want      string
	}{
		{name: "absent header keeps marker", setHeader: false, want: marker},
		{name: "off suppresses", setHeader: true, value: "off", want: ""},
		{name: "OFF is case-insensitive", setHeader: true, value: "OFF", want: ""},
		{name: "surrounding whitespace tolerated", setHeader: true, value: "  off  ", want: ""},
		{name: "false suppresses", setHeader: true, value: "false", want: ""},
		{name: "0 suppresses", setHeader: true, value: "0", want: ""},
		{name: "none suppresses", setHeader: true, value: "none", want: ""},
		{name: "on keeps marker", setHeader: true, value: "on", want: marker},
		{name: "unrecognized value keeps marker", setHeader: true, value: "yes", want: marker},
		{name: "empty value keeps marker", setHeader: true, value: "", want: marker},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := http.Header{}
			if tc.setHeader {
				h.Set(routingMarkerHeader, tc.value)
			}
			assert.Equal(t, tc.want, suppressMarkerIfRequested(context.Background(), h, marker))
		})
	}
}

// TestSuppressMarkerIfRequestedHiddenByInstallation pins that an installation
// with terminal surfaces hidden drops the marker regardless of the header.
func TestSuppressMarkerIfRequestedHiddenByInstallation(t *testing.T) {
	const marker = "✦ **Weave Router** → deepseek/deepseek-v4-flash · best pick for this turn\n\n"
	h := http.Header{}

	// Hidden: even an absent header (which would keep the marker) yields "".
	hiddenCtx := context.WithValue(context.Background(), InstallationHideTerminalSurfacesContextKey{}, true)
	assert.Equal(t, "", suppressMarkerIfRequested(hiddenCtx, h, marker))

	// Visible: same header state keeps the marker.
	assert.Equal(t, marker, suppressMarkerIfRequested(context.Background(), h, marker))
}
