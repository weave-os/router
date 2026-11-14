package main

import (
	"weave-os/router/internal/proxy"
	"weave-os/router/internal/router/policy"
	"weave-os/router/internal/routingpolicy"
)

func withRoutingPolicy(svc *proxy.Service, plans *policy.PlanResolver) *proxy.Service {
	return svc.WithRoutingPolicy(routingpolicy.New(proxy.NewCredentialBroker(svc), plans))
}
