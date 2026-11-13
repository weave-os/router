package aliasedproviderclient

import (
	"context"
	"net/http"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
)

type upstreamClient = providers.Client

func dispatch(ctx context.Context, client upstreamClient, decision router.Decision, prepared providers.PreparedRequest, writer http.ResponseWriter, request *http.Request) error {
	return client.Proxy(ctx, decision, prepared, writer, request)
}
