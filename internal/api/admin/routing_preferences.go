package admin

import (
	"net/http"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/observability"

	"github.com/gin-gonic/gin"
)

const (
	defaultQualityPct = 70.0
	defaultPricePct   = 30.0
)

type routingPreferencesResponse struct {
	Quality   float64 `json:"quality"`
	Price     float64 `json:"price"`
	IsDefault bool    `json:"is_default"`
}

type updateRoutingPreferencesRequest struct {
	Reset   bool    `json:"reset"`
	Quality float64 `json:"quality"`
	Price   float64 `json:"price"`
}

func defaultRoutingPreferences() routingPreferencesResponse {
	return routingPreferencesResponse{
		Quality:   defaultQualityPct,
		Price:     defaultPricePct,
		IsDefault: true,
	}
}

func routingPreferencesFor(installation *auth.Installation) routingPreferencesResponse {
	if installation.RoutingQualityWeight == nil {
		return defaultRoutingPreferences()
	}
	quality := *installation.RoutingQualityWeight
	price := 1.0 - quality
	if price < 0 {
		price = 0
	}
	return routingPreferencesResponse{
		Quality:   quality * 100,
		Price:     price * 100,
		IsDefault: false,
	}
}

func normalizeRoutingWeight(quality, price float64) (qualityWeight float64, ok bool) {
	if quality < 0 || price < 0 {
		return 0, false
	}
	total := quality + price
	if total <= 0 {
		return 0, false
	}
	return quality / total, true
}

func GetRoutingPreferencesHandler(authSvc *auth.Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		installation, ok := resolveInstallation(c, authSvc)
		if !ok {
			return
		}
		c.JSON(http.StatusOK, routingPreferencesFor(installation))
	}
}

func UpdateRoutingPreferencesHandler(authSvc *auth.Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		log := observability.FromGin(c)
		installation, ok := resolveInstallation(c, authSvc)
		if !ok {
			return
		}

		var req updateRoutingPreferencesRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
			return
		}

		if req.Reset {
			if err := authSvc.SetInstallationRoutingPreference(c.Request.Context(), installation.ExternalID, installation.ID, nil); err != nil {
				log.Error("Failed to clear routing preference", "err", err, "installation_id", installation.ID)
				c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "failed to update routing preference"})
				return
			}
			c.JSON(http.StatusOK, defaultRoutingPreferences())
			return
		}

		qualityWeight, ok := normalizeRoutingWeight(req.Quality, req.Price)
		if !ok {
			c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "at least one routing preference must be greater than zero"})
			return
		}

		if err := authSvc.SetInstallationRoutingPreference(c.Request.Context(), installation.ExternalID, installation.ID, &qualityWeight); err != nil {
			log.Error("Failed to update routing preference", "err", err, "installation_id", installation.ID)
			c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "failed to update routing preference"})
			return
		}

		priceWeight := 1.0 - qualityWeight
		if priceWeight < 0 {
			priceWeight = 0
		}
		c.JSON(http.StatusOK, routingPreferencesResponse{
			Quality:   qualityWeight * 100,
			Price:     priceWeight * 100,
			IsDefault: false,
		})
	}
}
