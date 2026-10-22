package proxy

import (
	"context"
	"net/http"

	"weave-os/router/internal/translate"
)

type feedbackDeliveryContextKey struct{}

// withFeedbackDelivery reuses the Responses ingress coordinator so its concrete
// ResponsesWriter and capture splice points remain visible during routing.
func withFeedbackDelivery(ctx context.Context, intent translate.FeedbackIntent) (context.Context, *translate.FeedbackDelivery) {
	if delivery, ok := ctx.Value(feedbackDeliveryContextKey{}).(*translate.FeedbackDelivery); ok {
		delivery.SetIntent(intent)
		return ctx, delivery
	}
	delivery := translate.NewFeedbackDelivery(ctx, intent)
	return context.WithValue(ctx, feedbackDeliveryContextKey{}, delivery), delivery
}

// feedbackProviderWriter is constructed for every actual provider attempt,
// before tool-name repair or cross-protocol conversion. Synthetic downstream
// content cannot feed this observer or the provider's progress/accounting path.
func feedbackProviderWriter(ctx context.Context, w http.ResponseWriter, format translate.Format, streaming bool) (http.ResponseWriter, func(error) error) {
	delivery, _ := ctx.Value(feedbackDeliveryContextKey{}).(*translate.FeedbackDelivery)
	if !delivery.Enabled() {
		return w, func(err error) error { return err }
	}
	observer := translate.NewCompletionObserver(w, format, streaming, delivery)
	return observer, observer.Finish
}
