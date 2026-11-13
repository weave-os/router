package policyclient

import (
	"context"

	"golang.org/x/sync/errgroup"
	"weave-os/router/internal/router/policy"
	"weave-os/router/internal/router/taskdomain"
)

// WithTaskDomain joins optional task inference before Go consumes complexity facts.
func WithTaskDomain(resolver taskdomain.Resolver) Option {
	return func(c *Client) { c.taskDomain = resolver }
}

func (c *Client) decideWithTaskDomain(ctx context.Context, query policy.Query) (policy.Result, error) {
	group, concurrentCtx := errgroup.WithContext(ctx)
	var classification policy.Result
	var task taskdomain.Outcome
	group.Go(func() error {
		var err error
		if query.ClassifierReleaseID != "" || query.ClassifierSelectionID != "" {
			classification, err = c.decideV5(concurrentCtx, query)
		} else {
			classification, err = c.decide(concurrentCtx, query)
		}
		return err
	})
	group.Go(func() error {
		task = c.taskDomain.Resolve(concurrentCtx, *query.TaskDomain)
		return nil
	})
	if err := group.Wait(); err != nil {
		return policy.Result{}, err
	}
	if err := ctx.Err(); err != nil {
		return policy.Result{}, err
	}
	classification.TaskDomain = &task
	return classification, nil
}
