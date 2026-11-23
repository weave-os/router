package main

import (
	"weave-os/router/internal/credentials"
	"weave-os/router/internal/server"
)

func withCredentialPorts(features server.Features) server.Features {
	features.SubscriptionCoverage = credentials.RequestPresentsCoveringSubscription
	features.CustomerFunding = credentials.RequestHasCustomerFunding
	return features
}
