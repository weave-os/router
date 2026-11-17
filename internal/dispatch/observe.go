package dispatch

import (
	"context"
	"net/http"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
)

func ObservePrepared(client providers.Client, observe func(ctx context.Context, body []byte)) providers.Client {
	return preparedObserver{Client: client, observe: observe}
}

type ProxyResponseObserver func(context.Context, router.Decision, providers.PreparedRequest, http.ResponseWriter) (http.ResponseWriter, func(error) error, error)

func ObserveProxyResponse(client providers.Client, observe ProxyResponseObserver, passthroughError error) providers.Client {
	return preparedObserver{Client: client, observeResponse: observe, passthroughError: passthroughError}
}

type preparedObserver struct {
	providers.Client
	observe          func(ctx context.Context, body []byte)
	observeResponse  ProxyResponseObserver
	passthroughError error
}

func (o preparedObserver) Proxy(ctx context.Context, decision router.Decision, prep providers.PreparedRequest, w http.ResponseWriter, r *http.Request) error {
	if o.observe != nil {
		o.observe(ctx, prep.Body)
	}
	var finish func(error) error
	if o.observeResponse != nil {
		var err error
		w, finish, err = o.observeResponse(ctx, decision, prep, w)
		if err != nil {
			return err
		}
	}
	err := o.Client.Proxy(ctx, decision, prep, w, r)
	if finish != nil {
		return finish(err)
	}
	return err
}

func (o preparedObserver) Passthrough(ctx context.Context, prep providers.PreparedRequest, w http.ResponseWriter, r *http.Request) error {
	if o.passthroughError != nil {
		return o.passthroughError
	}
	if o.observe != nil {
		o.observe(ctx, prep.Body)
	}
	return o.Client.Passthrough(ctx, prep, w, r)
}
