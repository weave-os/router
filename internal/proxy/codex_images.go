package proxy

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/requestcontext"
)

const (
	CodexImageGenerations = "generations"
	CodexImageEdits       = "edits"
)

var ErrCodexImagesRequireChatGPT = errors.New("image generation through the router requires signing in to Codex with ChatGPT")

type CodexImages struct {
	svc *Service
}

func NewCodexImages(svc *Service) CodexImages {
	return CodexImages{svc: svc}
}

func (c CodexImages) ForwardWithCallerChatGPTLogin(ctx context.Context, headers http.Header, operation string, body []byte, w http.ResponseWriter) error {
	if operation != CodexImageGenerations && operation != CodexImageEdits {
		return fmt.Errorf("unsupported Codex image operation %q", operation)
	}
	creds := ExtractClientCredentials(providers.ProviderOpenAI, headers)
	if creds == nil || !creds.OAuth || len(creds.AccountID) == 0 {
		return ErrCodexImagesRequireChatGPT
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
