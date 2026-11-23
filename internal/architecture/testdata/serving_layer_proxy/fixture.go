package servinglayerproxy

import "weave-os/router/internal/proxy"

type handlerDeps struct {
	svc *proxy.Service
}

var _ = handlerDeps{}
