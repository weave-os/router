package admin

import (
	"net/http"

	"weave-os/router/internal/policyregistry"
	"weave-os/router/internal/version"

	"github.com/gin-gonic/gin"
)

type versionResponse struct {
	Commit      string `json:"commit"`
	CommitShort string `json:"commit_short"`
	PR          string `json:"pr"`
	Display     string `json:"display"`
	BuildTime   string `json:"build_time"`
	// PolicyReleaseID and PolicySHA256 identify the stable-lane policy release
	// this replica serves; it can change without a binary deploy.
	PolicyReleaseID string `json:"policy_release_id,omitempty"`
	PolicySHA256    string `json:"policy_sha256,omitempty"`
}

// PolicyStatusSource reports the policy release a lane manager is serving.
type PolicyStatusSource interface {
	Status() policyregistry.ManagerStatus
}

// VersionHandler reports the running binary's build identity — the git commit
// it was built from and the build timestamp — plus the active policy release when policy is non-nil, so operators (and the
// managed-deployment badge in the README) can tell what is live. Unauthed and
// mounted in both deployment modes, like /health; every field is public
// metadata with no leak risk.
func VersionHandler(policy PolicyStatusSource) gin.HandlerFunc {
	return func(c *gin.Context) {
		response := versionResponse{
			Commit:      version.Commit,
			CommitShort: version.ShortCommit(),
			PR:          version.PR,
			Display:     version.Display(),
			BuildTime:   version.BuildTime,
		}
		if policy != nil {
			status := policy.Status()
			response.PolicyReleaseID = status.ActiveReleaseID
			response.PolicySHA256 = status.ActivePolicySHA256
		}
		c.JSON(http.StatusOK, response)
	}
}
