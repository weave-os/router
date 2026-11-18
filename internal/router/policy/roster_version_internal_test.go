package policy

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestServedRosterVersionIsTheAdmittedPolicyDigest(t *testing.T) {
	const (
		admitted = "admitted-policy-digest"
		sidecar  = "sidecar-roster"
	)
	tests := []struct {
		name              string
		admitted          string
		wantRosterVersion string
	}{
		{name: "Go selection reports the digest of the admitted policy that picked the arm", admitted: admitted, wantRosterVersion: admitted},
		{name: "sidecar selection reports the sidecar roster", wantRosterVersion: sidecar},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			assert.Equal(t, test.wantRosterVersion, servedRosterVersion(test.admitted, sidecar))
		})
	}
}
