package servinglayerproxy

import (
	"weave-os/router/internal/architecture/testdata/serving_layer_proxy/proxyref"
	"weave-os/router/internal/proxy"
)

type handlerDeps struct {
	svc *proxy.Service
}

type aliasDeps struct {
	svc proxyref.ServiceRef
}

type S = proxy.Service

type pointerAliasDeps struct {
	svc *S
}

var (
	_ = handlerDeps{}
	_ = aliasDeps{}
	_ = pointerAliasDeps{}
)
