package middleware

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"weave-os/router/internal/policyregistry"
)

func rejectMisdirectedChannelModel(c *gin.Context, cfg *ServingAdmissionConfig, body []byte) bool {
	if requestedChannelModel(c, body) != policyregistry.BetaChannelModel {
		return false
	}
	if channel, _ := cfg.Identity.Target.Channel(); channel == policyregistry.ChannelBeta {
		return false
	}
	response := gin.H{"error": "misdirected_serving_channel", "message": "weave-auto-beta is served only by the beta router host."}
	if cfg.BetaBaseURL != "" {
		response["base_url"] = cfg.BetaBaseURL
	}
	c.AbortWithStatusJSON(http.StatusMisdirectedRequest, response)
	return true
}

func requestedChannelModel(c *gin.Context, body []byte) string {
	if c.Request.Method != http.MethodPost {
		return ""
	}
	return requestedModel(c, body)
}

func servingChannelAdmissionError(cfg *ServingAdmissionConfig, err error) (int, gin.H, bool) {
	switch {
	case errors.Is(err, policyregistry.ErrServingChannelChanged):
		return http.StatusConflict, gin.H{"error": "start_new_thread_to_change_router"}, true
	case errors.Is(err, policyregistry.ErrServingChannelNotPermitted):
		channel, _ := cfg.Identity.Target.Channel()
		response := gin.H{"error": "serving_channel_not_permitted", "channel": string(channel), "message": "This installation does not have permission for the " + string(channel) + " router channel."}
		if cfg.StableBaseURL != "" {
			response["stable_base_url"] = cfg.StableBaseURL
		}
		return http.StatusForbidden, response, true
	}
	return 0, nil, false
}
