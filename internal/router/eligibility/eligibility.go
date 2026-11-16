package eligibility

import "errors"

type Source string

const (
	SourceOpenSource   Source = "open_source"
	SourceClosedSource Source = "closed_source"
	SourceUnknown      Source = "unknown"
)

func (s Source) Valid() bool {
	switch s {
	case SourceOpenSource, SourceClosedSource, SourceUnknown:
		return true
	default:
		return false
	}
}

func (s Source) String() string { return string(s) }

var ErrModelIneligible = errors.New("model is not eligible for this product")

type Product string

const ProductMaxSubscription Product = "max_subscription"

type Boundary struct {
	product   Product
	permitted map[Source]struct{}
}

var MaxOpenSourceOnly = New(ProductMaxSubscription, SourceOpenSource)

func New(product Product, permitted ...Source) Boundary {
	sources := make(map[Source]struct{}, len(permitted))
	for _, source := range permitted {
		sources[source] = struct{}{}
	}
	return Boundary{product: product, permitted: sources}
}

func Unrestricted() Boundary { return Boundary{} }

func (b Boundary) Restricts() bool { return b.permitted != nil }

func (b Boundary) Product() Product { return b.product }

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
