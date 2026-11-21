package main

import (
	"weave-os/router/internal/proxy"
	"weave-os/router/internal/server"
)

func withCredentialPorts(features server.Features) server.Features {
	features.SubscriptionCoverage = proxy.RequestPresentsCoveringSubscription
	features.CustomerFunding = proxy.RequestHasCustomerFunding
	return features
}
