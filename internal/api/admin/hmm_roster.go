package admin

import (
	"net/http"
	"sort"
	"strings"

	"weave-os/router/internal/observability"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/hmm"
	"weave-os/router/internal/router/policy"

	"github.com/gin-gonic/gin"
)

// hmmClusterDTO is one classifier cluster with its ordered default catalog
// roster arms and catalog model IDs (index 0 = highest serving priority).
type hmmClusterDTO struct {
	Cluster string `json:"cluster"`
	// Arms are the roster-native IDs, including provider prefixes and effort
	// suffixes. Models is the catalog-facing projection used by settings.
	Arms   []string `json:"arms"`
	Models []string `json:"models"`
}

type hmmRosterResponse struct {
	SchemaVersion            string                     `json:"schema_version"`
	ReleaseID                string                     `json:"release_id"`
	PolicySHA256             string                     `json:"policy_sha256"`
	RosterSHA256             string                     `json:"roster_sha256"`
	Environment              string                     `json:"environment"`
	Lane                     string                     `json:"lane"`
	HeadGeneration           int64                      `json:"head_generation"`
	LatestObservedGeneration int64                      `json:"latest_observed_generation"`
	RejectedGeneration       int64                      `json:"rejected_generation,omitempty"`
	LastRejectionReason      string                     `json:"last_rejection_reason,omitempty"`
	Clusters                 []hmmClusterDTO            `json:"clusters"`
	Harnesses                map[string][]hmmClusterDTO `json:"harnesses,omitempty"`
}

// HMMRosterHandler returns a live Go policy projection mapped from roster IDs
// to catalog model IDs. Unauthed — read-only and non-sensitive (same as /v1/router/models).
func HMMRosterHandler(sources map[router.Strategy]policy.RosterSource) gin.HandlerFunc {
	return func(c *gin.Context) {
		strategy := router.Strategy(strings.ToLower(strings.TrimSpace(c.Query("strategy"))))
		if strategy == "" || strategy == router.StrategyHMMEmbedding {
			strategy = router.StrategyHMM
		}
		if strategy != router.StrategyHMM && strategy != router.StrategyHMMBeta {
			c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "unsupported_hmm_strategy"})
			return
		}
		source := sources[strategy]
		if source == nil {
			c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{"error": "hmm_policy_lane_unavailable"})
			return
		}
		snapshot, err := source.ClusterRoster(c.Request.Context())
		if err != nil {
			observability.FromGin(c).Warn("HMM roster fetch failed", "err", err)
			c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{"error": "hmm_roster_unavailable"})
			return
		}
		c.JSON(http.StatusOK, hmmRosterResponse{
			SchemaVersion: snapshot.SchemaVersion, ReleaseID: snapshot.ReleaseID,
			PolicySHA256: snapshot.PolicySHA256, RosterSHA256: snapshot.RosterSHA256,
			Environment: snapshot.Environment, Lane: snapshot.Lane, HeadGeneration: snapshot.HeadGeneration,
			LatestObservedGeneration: snapshot.LatestObservedGeneration, RejectedGeneration: snapshot.RejectedGeneration,
			LastRejectionReason: snapshot.LastRejectionReason, Clusters: rosterClusterDTOs(snapshot.Clusters),
			Harnesses: rosterHarnessDTOs(snapshot.Harnesses),
		})
	}
}

func rosterClusterDTOs(clusters map[string][]string) []hmmClusterDTO {
	projection := make([]hmmClusterDTO, 0, len(clusters))
	for cluster, arms := range clusters {
		models := make([]string, 0, len(arms))
		for _, arm := range arms {
			models = append(models, hmm.CatalogIDForRoster(arm))
		}
		projection = append(projection, hmmClusterDTO{Arms: append([]string(nil), arms...), Cluster: cluster, Models: models})
	}
	sort.SliceStable(projection, func(i, j int) bool { return projection[i].Cluster < projection[j].Cluster })
	return projection
}

func rosterHarnessDTOs(harnesses map[string]map[string][]string) map[string][]hmmClusterDTO {
	if len(harnesses) == 0 {
		return nil
	}
	projection := make(map[string][]hmmClusterDTO, len(harnesses))
	for harness, clusters := range harnesses {
		projection[harness] = rosterClusterDTOs(clusters)
	}
	return projection
}
