package middleware

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"weave-os/router/internal/policyregistry"
	"weave-os/router/internal/router"
)

func rejectMisdirectedChannelModel(c *gin.Context, cfg *ServingAdmissionConfig, body []byte) bool {
	if router.RoutingModel(requestedChannelModel(c, body)) != router.ModelAutoBeta || servesBetaChannel(cfg.Identity.Target) {
		return false
	}
	abortMisdirectedChannelModel(c, cfg.BetaBaseURL)
	return true
}

func servesBetaChannel(target policyregistry.ServingTarget) bool {
	channel, _ := target.Channel()
	return channel == policyregistry.ChannelBeta
}

func abortMisdirectedChannelModel(c *gin.Context, betaBaseURL string) {
	response := gin.H{"error": "misdirected_serving_channel", "message": string(router.ModelAutoBeta) + " is served only by the beta router host."}
	if betaBaseURL != "" {
		response["base_url"] = betaBaseURL
	}
	c.AbortWithStatusJSON(http.StatusMisdirectedRequest, response)
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
