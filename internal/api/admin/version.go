package admin

import (
	"context"
	"net/http"

	"weave-os/router/internal/policyregistry"
	"weave-os/router/internal/version"

	"github.com/gin-gonic/gin"
)

type versionResponse struct {
	Commit              string         `json:"commit"`
	CommitShort         string         `json:"commit_short"`
	PR                  string         `json:"pr"`
	Display             string         `json:"display"`
	BuildTime           string         `json:"build_time"`
	PolicyReleaseID     string         `json:"policy_release_id,omitempty"`
	PolicySHA256        string         `json:"policy_sha256,omitempty"`
	RouterSchemaVersion *int64         `json:"router_schema_version,omitempty"`
	ServingSource       *ServingSource `json:"serving_source,omitempty"`
}

type ServingSource struct {
	Target             string `json:"target"`
	ActivationID       string `json:"activation_id"`
	SelectionSetSHA256 string `json:"selection_set_sha256"`
}

type SchemaVersionSource func(context.Context) (int64, error)

type VersionDetails struct {
	SchemaVersion SchemaVersionSource
	ServingSource *ServingSource
}

type PolicyStatusSource interface {
	Status() policyregistry.ManagerStatus
}

func VersionHandler(policy PolicyStatusSource, details VersionDetails) gin.HandlerFunc {
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
		if details.SchemaVersion != nil {
			schemaVersion, err := details.SchemaVersion(c.Request.Context())
			if err == nil {
				response.RouterSchemaVersion = &schemaVersion
			}
		}
		response.ServingSource = details.ServingSource
		c.JSON(http.StatusOK, response)
	}
}
