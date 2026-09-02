package admin

import (
	"fmt"
	"net/http"
	"sort"
	"time"

	"workweave/router/internal/providers"
	"workweave/router/internal/router/catalog"
	"workweave/router/internal/router/modelstatus"

	"github.com/gin-gonic/gin"
)

type modelStatusEntry struct {
	ModelID     string     `json:"model_id"`
	Provider    string     `json:"provider"`
	Status      string     `json:"status"`
	Reason      string     `json:"reason"`
	Source      string     `json:"source"`
	UpdatedAt   time.Time  `json:"updated_at"`
	ExpiresAt   *time.Time `json:"expires_at"`
	AdminPinned bool       `json:"admin_pinned"`
	Wired       bool       `json:"wired"`
}

func statusResponse(entry modelstatus.Entry) modelStatusEntry {
	var expiresAt *time.Time
	if !entry.ExpiresAt.IsZero() {
		value := entry.ExpiresAt
		expiresAt = &value
	}
	return modelStatusEntry{ModelID: entry.ModelID, Provider: entry.Provider, Status: entry.Status.String(), Reason: entry.Reason, Source: string(entry.Source), UpdatedAt: entry.UpdatedAt, ExpiresAt: expiresAt, AdminPinned: entry.AdminPinned, Wired: entry.Wired}
}

// GetModelStatusHandler lists binding statuses with optional exact filters.
func GetModelStatusHandler(store *modelstatus.Store) gin.HandlerFunc {
	return func(c *gin.Context) {
		statusFilter := c.Query("status")
		if statusFilter != "" {
			if _, err := modelstatus.ParseStatus(statusFilter); err != nil {
				c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
				return
			}
		}
		entries := make([]modelStatusEntry, 0)
		for _, entry := range store.Snapshot(c.Request.Context()) {
			if model := c.Query("model_id"); model != "" && entry.ModelID != model {
				continue
			}
			if provider := c.Query("provider"); provider != "" && entry.Provider != provider {
				continue
			}
			if statusFilter != "" && entry.Status.String() != statusFilter {
				continue
			}
			entries = append(entries, statusResponse(entry))
		}
		c.JSON(http.StatusOK, gin.H{"generated_at": time.Now().UTC(), "total": len(entries), "entries": entries})
	}
}

type updateModelStatusRequest struct {
	ModelID  string `json:"model_id" binding:"required"`
	Provider string `json:"provider" binding:"required"`
	Status   string `json:"status" binding:"required"`
	Reason   string `json:"reason"`
}

// UpdateModelStatusHandler applies or resets an administrative override.
func UpdateModelStatusHandler(store *modelstatus.Store) gin.HandlerFunc {
	return func(c *gin.Context) {
		var request updateModelStatusRequest
		if err := c.ShouldBindJSON(&request); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
			return
		}
		model, ok := catalog.ByID(request.ModelID)
		if !ok {
			c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("unknown model: %s", request.ModelID)})
			return
		}
		bindingFound := false
		for _, binding := range model.Providers {
			if binding.Provider == request.Provider {
				bindingFound = true
				break
			}
		}
		if !bindingFound {
			c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("model %s has no binding on provider %s", request.ModelID, request.Provider)})
			return
		}
		key := modelstatus.Key{ModelID: model.ID, Provider: request.Provider}
		current, exists := store.Get(c.Request.Context(), key)
		if !exists {
			c.JSON(http.StatusBadRequest, gin.H{"error": "model binding status is not initialized"})
			return
		}
		if request.Status == "auto" {
			entry, _ := store.ResetToAuto(c.Request.Context(), key)
			c.JSON(http.StatusOK, statusResponse(entry))
			return
		}
		status, err := modelstatus.ParseStatus(request.Status)
		if err != nil || status == modelstatus.StatusRateLimited {
			c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("invalid status: %s", request.Status)})
			return
		}
		if status == modelstatus.StatusOnline && !current.Wired {
			c.JSON(http.StatusBadRequest, gin.H{"error": "status online requires a wired provider"})
			return
		}
		entry := store.SetStatus(c.Request.Context(), key, status, request.Reason, modelstatus.SourceAdmin, true, 0)
		c.JSON(http.StatusOK, statusResponse(entry))
	}
}

type inventoryBinding struct {
	ModelID         string     `json:"model_id"`
	UpstreamID      string     `json:"upstream_id"`
	Tier            string     `json:"tier"`
	ContextWindow   int        `json:"context_window"`
	PriceInput      float64    `json:"price_input_per_1m_usd"`
	PriceOutput     float64    `json:"price_output_per_1m_usd"`
	Status          string     `json:"status"`
	StatusReason    string     `json:"status_reason"`
	StatusSource    string     `json:"status_source"`
	StatusUpdatedAt time.Time  `json:"status_updated_at"`
	StatusExpiresAt *time.Time `json:"status_expires_at"`
	AdminPinned     bool       `json:"admin_pinned"`
}

type providerInventory struct {
	Provider             string             `json:"provider"`
	Family               string             `json:"family"`
	APIKeyEnv            string             `json:"api_key_env"`
	DeploymentKeyPresent bool               `json:"deployment_key_present"`
	IsGateway            bool               `json:"is_gateway"`
	IsCredentialOnly     bool               `json:"is_credential_only"`
	Bindings             []inventoryBinding `json:"bindings"`
}

// ProviderInventoryHandler returns every known provider and catalog binding.
func ProviderInventoryHandler(store *modelstatus.Store) gin.HandlerFunc {
	return func(c *gin.Context) {
		rows := make(map[modelstatus.Key]modelstatus.Entry)
		for _, entry := range store.Snapshot(c.Request.Context()) {
			rows[entry.Key] = entry
		}
		inventory := make([]providerInventory, 0, len(providers.AllProviders()))
		for _, provider := range providers.AllProviders() {
			item := providerInventory{Provider: provider, Family: providers.FamilyFor(provider).String(), APIKeyEnv: providers.APIKeyEnvVar(provider), IsGateway: providers.IsGateway(provider), IsCredentialOnly: providers.IsCredentialOnly(provider), Bindings: []inventoryBinding{}}
			for _, model := range catalog.Models {
				for _, binding := range model.Providers {
					if binding.Provider != provider {
						continue
					}
					entry := rows[modelstatus.Key{ModelID: model.ID, Provider: provider}]
					status := statusResponse(entry)
					item.DeploymentKeyPresent = item.DeploymentKeyPresent || entry.Wired
					item.Bindings = append(item.Bindings, inventoryBinding{ModelID: model.ID, UpstreamID: catalog.UpstreamIDFor(model.ID, binding.UpstreamID), Tier: model.Tier.String(), ContextWindow: catalog.ContextWindowForBinding(model.ID, provider), PriceInput: binding.Price.InputUSDPer1M, PriceOutput: binding.Price.OutputUSDPer1M, Status: status.Status, StatusReason: status.Reason, StatusSource: status.Source, StatusUpdatedAt: status.UpdatedAt, StatusExpiresAt: status.ExpiresAt, AdminPinned: status.AdminPinned})
				}
			}
			sort.Slice(item.Bindings, func(i, j int) bool { return item.Bindings[i].ModelID < item.Bindings[j].ModelID })
			inventory = append(inventory, item)
		}
		c.JSON(http.StatusOK, gin.H{"generated_at": time.Now().UTC(), "providers": inventory})
	}
}
