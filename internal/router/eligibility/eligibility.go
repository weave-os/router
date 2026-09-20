// Package eligibility expresses hard product boundaries on which models a
// request may dispatch, in terms of how a model's weights are published. A
// boundary is a property of what the customer bought, not a routing
// preference: it cannot be widened by request-time allowlists, by prepaid
// balance, or by any other funding path, and it is enforced before candidate
// and provider resolution.
//
// It is a leaf package deliberately: the catalog imports it to type its per
// model classification, and internal/router carries a Boundary on Request, so
// it must depend on neither. Catalog-aware helpers (classify an ID, list the
// models a boundary refuses) live in internal/router/catalog.
package eligibility

import "errors"

// Source records how a model's weights are published. It is product data, not
// routing data: plans that sell an open-source-only boundary decide dispatch
// eligibility from it, so an unclassified model must never read as permitted.
// Every catalog row carries one explicitly and catalog tests reject a missing
// or unrecognized value.
type Source string

const (
	// SourceOpenSource marks a model whose weights are publicly released under
	// a license permitting self-hosting (DeepSeek, Qwen, GLM, Kimi, Gemma...).
	SourceOpenSource Source = "open_source"
	// SourceClosedSource marks a proprietary model served only from its
	// vendor's API (Claude, GPT, Gemini, Grok...).
	SourceClosedSource Source = "closed_source"
	// SourceUnknown marks a model whose weight release we have not confirmed.
	// It is deliberately distinct from closed source: it records missing
	// evidence rather than a known answer. Open-source-only products treat it
	// exactly as closed — it is never eligible.
	SourceUnknown Source = "unknown"
)

// Valid reports whether the classification is one of the recognized values.
func (s Source) Valid() bool {
	switch s {
	case SourceOpenSource, SourceClosedSource, SourceUnknown:
		return true
	default:
		return false
	}
}

// String renders the classification for logs and diagnostics.
func (s Source) String() string { return string(s) }

// ErrModelIneligible is the sentinel every refusal wraps, so callers can map
// the whole class to one response without knowing which boundary refused.
var ErrModelIneligible = errors.New("model is not eligible for this product")

// Product names the purchased boundary for diagnostics and error text.
type Product string

// ProductMaxSubscription is the individual Max plan: open-source models only.
const ProductMaxSubscription Product = "max_subscription"

// Boundary is the set of source classifications a product may dispatch. The
// zero value is unrestricted — requests carrying no product boundary route
// exactly as they did before one existed.
//
// A boundary fails closed: an unclassified source is refused rather than waved
// through, and so (via the catalog helpers) is a model with no catalog row at
// all, so adding a model without classifying it can only lose eligibility.
type Boundary struct {
	product   Product
	permitted map[Source]struct{}
}

// MaxOpenSourceOnly is Max's product boundary. Closed-source and unclassified
// models are impossible to dispatch under it, however the turn is funded.
var MaxOpenSourceOnly = New(ProductMaxSubscription, SourceOpenSource)

// New builds a boundary permitting exactly the listed sources. Passing none
// yields a boundary that permits nothing, not an unrestricted one.
func New(product Product, permitted ...Source) Boundary {
	sources := make(map[Source]struct{}, len(permitted))
	for _, source := range permitted {
		sources[source] = struct{}{}
	}
	return Boundary{product: product, permitted: sources}
}

// Unrestricted is the boundary of a request that bought no product boundary.
func Unrestricted() Boundary { return Boundary{} }

// Restricts reports whether the boundary constrains dispatch at all.
func (b Boundary) Restricts() bool { return b.permitted != nil }

// Product returns the purchased product, empty when unrestricted.
func (b Boundary) Product() Product { return b.product }

// PermitsSource reports whether models with this classification may dispatch.
func (b Boundary) PermitsSource(source Source) bool {
	if !b.Restricts() {
		return true
	}
	if !source.Valid() {
		return false
	}
	_, permitted := b.permitted[source]
	return permitted
}
