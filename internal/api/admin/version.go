package admin

import (
	"net/http"

	"weave-os/router/internal/policyregistry"
	"weave-os/router/internal/version"

	"github.com/gin-gonic/gin"
)

type versionResponse struct {
	Commit          string `json:"commit"`
	CommitShort     string `json:"commit_short"`
	PR              string `json:"pr"`
	Display         string `json:"display"`
	BuildTime       string `json:"build_time"`
	PolicyReleaseID string `json:"policy_release_id,omitempty"`
	PolicySHA256    string `json:"policy_sha256,omitempty"`
}

type PolicyStatusSource interface {
	Status() policyregistry.ManagerStatus
}

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
