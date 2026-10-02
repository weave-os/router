package admin

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sort"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"weave-os/router/internal/observability"
	"weave-os/router/internal/policyregistry"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/cluster"
	"weave-os/router/internal/router/hmm"
	"weave-os/router/internal/router/hmm/rosterdata"
	hmmselection "weave-os/router/internal/router/hmm/selection"
	"weave-os/router/internal/router/policy"
)

const (
	maxDiscoveryBodyBytes  = 16 << 10
	maxDiscoveryExclusions = 128
)

type CurrentPolicyReader interface {
	ReadCurrentPolicy(context.Context, policyregistry.ServingTarget, string) (policyregistry.CurrentPolicy, error)
}

type routingDiscoveryRequest struct {
	Target            policyregistry.ServingTarget `json:"target"`
	ProfileKey        string                       `json:"profile_key,omitempty"`
	Grid              *int                         `json:"grid,omitempty"`
	ExcludedModels    []string                     `json:"excluded_models,omitempty"`
	ExcludedProviders []string                     `json:"excluded_providers,omitempty"`
}

type routingDiscoveryResponse struct {
	Target             policyregistry.ServingTarget `json:"target"`
	ProfileKey         string                       `json:"profile_key,omitempty"`
	ActivationID       string                       `json:"activation_id"`
	SelectionSetSHA256 string                       `json:"selection_set_sha256"`
	CandidateSHA256    string                       `json:"candidate_sha256"`
	PolicySHA256       string                       `json:"policy_sha256"`
	Clusters           []hmmClusterDTO              `json:"clusters"`
	Harnesses          map[string][]hmmClusterDTO   `json:"harnesses,omitempty"`
	Models             []catalog.ModelListing       `json:"models"`
	Catalog            []catalog.ModelListing       `json:"catalog"`
	Distribution       []cluster.DistributionPoint  `json:"distribution"`
}

// InternalRoutingDiscoveryHandler projects the current admitted managed policy.
// The caller is authenticated by the /internal/v1 group's service token.
func InternalRoutingDiscoveryHandler(source CurrentPolicyReader, availableProviders map[string]struct{}, workerIdentity *policyregistry.WorkerIdentity) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("Cache-Control", "no-store")
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxDiscoveryBodyBytes)
		decoder := json.NewDecoder(c.Request.Body)
		decoder.DisallowUnknownFields()
		var request routingDiscoveryRequest
		if err := decoder.Decode(&request); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid discovery request"})
			return
		}
		if err := decoder.Decode(new(json.RawMessage)); err != io.EOF {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid discovery request"})
			return
		}
		if _, err := request.Target.Environment(); err != nil || invalidDiscoveryProfile(request.ProfileKey) || invalidDiscoveryExclusions(request.ExcludedModels) || invalidDiscoveryExclusions(request.ExcludedProviders) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid discovery target, profile or exclusions"})
			return
		}
		for _, modelID := range request.ExcludedModels {
			if !catalogModelExists(modelID) {
				c.JSON(http.StatusBadRequest, gin.H{"error": "unknown excluded model"})
				return
			}
		}
		for _, providerID := range request.ExcludedProviders {
			if !catalogProviderExists(providerID) {
				c.JSON(http.StatusBadRequest, gin.H{"error": "unknown excluded provider"})
				return
			}
		}
		gridN := 0
		if request.Grid != nil {
			gridN = *request.Grid
			if gridN < 2 || gridN > maxDistributionGrid {
				c.JSON(http.StatusBadRequest, gin.H{"error": "grid must be an integer in [2, 101]"})
				return
			}
		}
		if source == nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "managed policy unavailable"})
			return
		}
		current, err := source.ReadCurrentPolicy(c.Request.Context(), request.Target, request.ProfileKey)
		if err != nil {
			observability.FromGin(c).Warn("Managed routing discovery policy unavailable", "target", request.Target, "profile_key", request.ProfileKey, "err", err)
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "managed policy unavailable"})
			return
		}
		if workerIdentity != nil {
			if err := workerIdentity.ValidateBinding(current.Target, current.Binding); err != nil {
				c.JSON(http.StatusServiceUnavailable, gin.H{"error": "managed policy unavailable on this worker"})
				return
			}
		}
		excludedModels := discoverySet(request.ExcludedModels)
		excludedProviders := discoverySet(request.ExcludedProviders)
		points, err := hmmselection.RoutingDistribution(current.Roster, gridN, availableProviders, excludedModels, excludedProviders)
		if errors.Is(err, cluster.ErrNoEligibleProvider) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "exclusions leave no eligible models"})
			return
		}
		if err != nil {
			observability.FromGin(c).Error("Managed routing discovery projection failed", "target", request.Target, "profile_key", request.ProfileKey, "policy_sha256", current.PolicySHA256, "err", err)
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "managed projection unavailable"})
			return
		}
		clusters, harnesses, models := discoveryRoster(current, availableProviders)
		managedProviderPolicy := policy.ManagedProviderPolicy()
		catalogModels := make([]catalog.ModelListing, 0)
		for _, model := range catalog.BindingListing() {
			if _, wired := availableProviders[model.Provider]; !wired || !managedProviderPolicy.Allows(model.Provider) {
				continue
			}
			catalogModels = append(catalogModels, model)
		}
		c.JSON(http.StatusOK, routingDiscoveryResponse{
			Target: current.Target, ProfileKey: current.ProfileKey, ActivationID: current.ActivationID,
			SelectionSetSHA256: current.SelectionSetSHA256, CandidateSHA256: current.CandidateSHA256,
			PolicySHA256: current.PolicySHA256, Clusters: clusters, Harnesses: harnesses, Models: models,
			Catalog: catalogModels, Distribution: points,
		})
	}
}

func invalidDiscoveryProfile(profileKey string) bool {
	if profileKey == "" {
		return false
	}
	parsed, err := uuid.Parse(profileKey)
	return err != nil || parsed == uuid.Nil || parsed.String() != profileKey
}

func invalidDiscoveryExclusions(ids []string) bool {
	if len(ids) > maxDiscoveryExclusions {
		return true
	}
	seen := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		if id == "" || strings.TrimSpace(id) != id || len(id) > 256 {
			return true
		}
		if _, duplicate := seen[id]; duplicate {
			return true
		}
		seen[id] = struct{}{}
	}
	return false
}

func discoverySet(ids []string) map[string]struct{} {
	set := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		set[id] = struct{}{}
	}
	return set
}

func catalogProviderExists(providerID string) bool {
	for _, model := range catalog.Models {
		for _, binding := range model.Providers {
			if binding.Provider == providerID {
				return true
			}
		}
	}
	return false
}

func catalogModelExists(modelID string) bool {
	for _, model := range catalog.Models {
		if model.ID == modelID {
			return true
		}
	}
	return false
}

func discoveryRoster(current policyregistry.CurrentPolicy, availableProviders map[string]struct{}) ([]hmmClusterDTO, map[string][]hmmClusterDTO, []catalog.ModelListing) {
	clusterNames := make([]string, 0, len(current.Roster.Clusters))
	for name := range current.Roster.Clusters {
		clusterNames = append(clusterNames, name)
	}
	sort.Strings(clusterNames)
	clusterArms := make(map[string][]string, len(clusterNames))
	harnessArms := make(map[string]map[string][]string)
	modelByID := make(map[string]catalog.Model)
	for _, name := range clusterNames {
		clusterRoster := current.Roster.Clusters[name]
		defaultArms, _ := hmmselection.ArmOrder(clusterRoster, "")
		clusterArms[name] = append([]string(nil), defaultArms...)
		armsToList := append([]string(nil), defaultArms...)
		for harness, arms := range clusterRoster.ArmsByHarness {
			if harness == rosterdata.HarnessAll || len(arms) == 0 {
				continue
			}
			harnessKey := string(harness)
			if harnessArms[harnessKey] == nil {
				harnessArms[harnessKey] = make(map[string][]string)
			}
			harnessArms[harnessKey][name] = append([]string(nil), arms...)
			armsToList = append(armsToList, arms...)
		}
		for _, arm := range armsToList {
			baseRosterID, _ := hmm.SplitEffort(arm)
			catalogID := hmm.CatalogIDForRoster(baseRosterID)
			if model, ok := catalog.ByID(catalogID); ok {
				modelByID[catalogID] = model
			}
		}
	}
	clusters := rosterClusterDTOs(clusterArms)
	harnesses := rosterHarnessDTOs(harnessArms)
	models := make([]catalog.ModelListing, 0, len(modelByID))
	managedProviderPolicy := policy.ManagedProviderPolicy()
	for catalogID, model := range modelByID {
		for _, binding := range model.Providers {
			if _, wired := availableProviders[binding.Provider]; !wired || !managedProviderPolicy.Allows(binding.Provider) {
				continue
			}
			models = append(models, catalog.ModelListing{
				Model: catalogID, Provider: binding.Provider, FastMode: catalog.SupportsFastMode(catalogID),
			})
		}
	}
	catalog.SortListing(models)
	return clusters, harnesses, models
}
