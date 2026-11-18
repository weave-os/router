package proxy

import "log/slog"

type bypassCompletionLog struct {
	externalID       string
	requestedModel   string
	decisionModel    string
	proxyMs          int64
	totalMs          int64
	proxyErr         error
	stopReason       string
	refusal          *refusalObserver
	credentialSource string
	subscriptionTier string
}

func (c bypassCompletionLog) emit(log *slog.Logger) {
	log.Info("ProxyMessages usage-bypass complete",
		"external_id", c.externalID,
		"requested_model", c.requestedModel,
		"decision_model", c.decisionModel,
		"proxy_ms", c.proxyMs,
		"total_ms", c.totalMs,
		"proxy_err", c.proxyErr,
		"resp_stop_reason", c.stopReason,
		"resp_refusal", c.refusal.refused,
		"resp_refusal_category", c.refusal.category,
		"credential_source", c.credentialSource,
		"subscription_tier", c.subscriptionTier,
	)
}
