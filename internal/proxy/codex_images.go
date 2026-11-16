package proxy

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"weave-os/router/internal/credentials"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/requestcontext"
)

const (
	CodexImageGenerations = "generations"
	CodexImageEdits       = "edits"
)

var (
	ErrCodexImagesRequireOwnCredential   = errors.New("image generation through the router requires signing in to Codex with ChatGPT, an OpenAI API key, or an OpenAI key saved in Weave")
	ErrCodexImagesUnavailableWithGateway = errors.New("image generation through the router is unavailable while a gateway key serves this installation")
)

type CodexImages struct {
	svc *Service
}

func NewCodexImages(svc *Service) CodexImages {
	return CodexImages{svc: svc}
}

func (c CodexImages) ForwardWithCallerCredential(ctx context.Context, headers http.Header, operation string, body []byte, w http.ResponseWriter) error {
	if operation != CodexImageGenerations && operation != CodexImageEdits {
		return fmt.Errorf("unsupported Codex image operation %q", operation)
	}
	creds, err := codexImageCredential(ctx, headers)
	if err != nil {
		return err
	}
	client, err := c.svc.provider(providers.ProviderOpenAI)
	if err != nil {
		return err
	}
	forwarder, ok := client.(interface {
		ForwardCodexImageRequest(context.Context, string, []byte, http.Header, http.ResponseWriter) error
	})
	if !ok {
		return fmt.Errorf("OpenAI provider does not support Codex image requests")
	}
	return forwarder.ForwardCodexImageRequest(requestcontext.WithCredentials(ctx, creds), operation, body, headers, w)
}

func codexImageCredential(ctx context.Context, headers http.Header) (*Credentials, error) {
	if len(credentials.GatewayProviders(externalKeysFromContext(ctx))) > 0 {
		return nil, ErrCodexImagesUnavailableWithGateway
	}
	if creds := ExtractClientCredentials(providers.ProviderOpenAI, headers); creds != nil {
		return creds, nil
	}
	if byok := usableBYOK(ctx, providers.ProviderOpenAI); byok != nil && byok.BaseURL == "" {
		return byok, nil
	}
	return nil, ErrCodexImagesRequireOwnCredential
}
