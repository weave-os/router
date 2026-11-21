package requestcontext

import "context"

type valuesHidden struct{ context.Context }

func (valuesHidden) Value(any) any { return nil }

func WithoutValues(ctx context.Context) context.Context {
	return valuesHidden{ctx}
}
