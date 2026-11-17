package proxy

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"

	"weave-os/router/internal/billing"
	"weave-os/router/internal/translate"
)

const reviewerAttemptsPerModel = 2

var autoModeReviewerModels = []string{"claude-sonnet-5", "claude-sonnet-4-6", "claude-haiku-5-5"}

type bypassAttempt func(env *translate.RequestEnvelope, feats translate.RoutingFeatures, w http.ResponseWriter) error

type reviewerOutcome struct {
	buf   *reviewerResponseBuffer
	err   error
	final bool
}

func serveAutoModeReviewer(ctx context.Context, body []byte, env *translate.RequestEnvelope, feats translate.RoutingFeatures, reviewer bool, w http.ResponseWriter, attempt bypassAttempt) error {
	if !reviewer || env.Stream() {
		return attempt(env, feats, w)
	}
	var out reviewerOutcome
	for i, model := range reviewerModelsFor(env.Model()) {
		if i > 0 {
			next, err := reviewerEnvelope(body, model)
			if err != nil {
				return err
			}
			env, feats = next, next.RoutingFeatures()
		}
		if out = attemptReviewerModel(ctx, env, feats, attempt); out.final {
			break
		}
	}
	if out.err != nil {
		return out.err
	}
	return out.buf.replay(w)
}

func attemptReviewerModel(ctx context.Context, env *translate.RequestEnvelope, feats translate.RoutingFeatures, attempt bypassAttempt) reviewerOutcome {
	var out reviewerOutcome
	for range reviewerAttemptsPerModel {
		out = reviewerOutcome{buf: newReviewerResponseBuffer()}
		out.err = attempt(env, feats, out.buf)
		rerouted := errors.Is(out.err, errBypassRetryable)
		switch {
		case out.err == nil && out.buf.verdict():
			out.final = true
			return out
		case rerouted && !billing.SubscriptionOnlyFromContext(ctx):
			out.final = true
			return out
		case !rerouted && !out.buf.transient():
			return out
		}
	}
	return out
}

func reviewerModelsFor(requested string) []string {
	models := []string{requested}
	for _, model := range autoModeReviewerModels {
		if model != requested {
			models = append(models, model)
		}
	}
	return models
}

func reviewerEnvelope(body []byte, model string) (*translate.RequestEnvelope, error) {
	rewritten, err := sjson.SetBytes(body, "model", model)
	if err != nil {
		return nil, fmt.Errorf("auto-mode reviewer fallback model: %w", err)
	}
	return translate.ParseAnthropic(rewritten)
}

type reviewerResponseBuffer struct {
	header http.Header
	status int
	body   bytes.Buffer
}

func newReviewerResponseBuffer() *reviewerResponseBuffer {
	return &reviewerResponseBuffer{header: http.Header{}}
}

func (b *reviewerResponseBuffer) Header() http.Header { return b.header }

func (b *reviewerResponseBuffer) WriteHeader(status int) {
	if b.status == 0 {
		b.status = status
	}
}

func (b *reviewerResponseBuffer) Write(p []byte) (int, error) {
	b.WriteHeader(http.StatusOK)
	return b.body.Write(p)
}

func (b *reviewerResponseBuffer) Flush() {}

func (b *reviewerResponseBuffer) verdict() bool {
	return b.status == http.StatusOK &&
		gjson.GetBytes(b.body.Bytes(), "stop_reason").String() != anthropicRefusalStopReason &&
		gjson.GetBytes(b.body.Bytes(), "content.#").Int() > 0
}

func (b *reviewerResponseBuffer) transient() bool {
	switch b.status {
	case http.StatusTooManyRequests, http.StatusInternalServerError, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout, 529:
		return true
	}
	return false
}

func (b *reviewerResponseBuffer) replay(w http.ResponseWriter) error {
	for key, values := range b.header {
		w.Header()[key] = values
	}
	if b.status != 0 {
		w.WriteHeader(b.status)
	}
	_, err := w.Write(b.body.Bytes())
	return err
}

func bypassAttemptFor(s *Service, ctx context.Context, routeRes turnLoopResult, requestStart time.Time, requestID, externalID string, r *http.Request) bypassAttempt {
	return func(env *translate.RequestEnvelope, feats translate.RoutingFeatures, w http.ResponseWriter) error {
		return s.bypassToAnthropic(ctx, env, feats, routeRes.modelSwitched(), requestStart, requestID, externalID, routeRes.TurnType, routeRes.Decision.Reason, r, w)
	}
}
