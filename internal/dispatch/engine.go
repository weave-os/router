package dispatch

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/policy"
)

type Clock interface {
	Now() time.Time
}

type Engine struct {
	clients               *Clients
	executor              *Executor
	defaultExecutorOnce   sync.Once
	plans                 *policy.PlanResolver
	defaultPlansOnce      sync.Once
	deployment            policy.DeploymentPolicyConfig
	gatewayCaps           GatewayCapabilities
	unservedGatewayModels sync.Map
	retrySleep            func(context.Context, time.Duration) error
}

func NewEngine(clients *Clients) *Engine {
	return &Engine{clients: clients}
}

func (e *Engine) Clients() *Clients {
	return e.clients
}

func (e *Engine) SetExecutor(executor *Executor) {
	e.executor = executor
	if executor != nil && executor.Clients() != nil {
		e.clients = executor.Clients()
	}
}

func (e *Engine) Executor() *Executor {
	return e.executor
}

func (e *Engine) ResolveExecutor(clock Clock) (*Executor, error) {
	if e.executor != nil {
		return e.executor, nil
	}
	var err error
	e.defaultExecutorOnce.Do(func() {
		opts := []Option{WithClock(clock.Now)}
		if e.retrySleep != nil {
			opts = append(opts, WithSleep(e.retrySleep))
		}
		e.executor, err = NewExecutor(e.clients, opts...)
	})
	if e.executor == nil {
		if err == nil {
			err = errors.New("dispatch executor unavailable")
		}
		return nil, err
	}
	return e.executor, nil
}

func (e *Engine) SetPlans(plans *policy.PlanResolver) {
	e.plans = plans
}

func (e *Engine) ResolvePlans() (*policy.PlanResolver, error) {
	if e.plans != nil {
		return e.plans, nil
	}
	var err error
	e.defaultPlansOnce.Do(func() {
		var plans *policy.PlanResolver
		plans, err = policy.NewPlanResolver(policy.DefaultRegistry(), policy.NewResolver(
			nil, nil, func(model catalog.Model) string { return model.ID }, policy.ProviderPolicy{}))
		if err == nil {
			e.plans = plans
		}
	})
	if err != nil {
		return nil, fmt.Errorf("inference plan resolver: %w", err)
	}
	if e.plans == nil {
		return nil, errors.New("inference plan resolver unavailable")
	}
	return e.plans, nil
}

func (e *Engine) SetDeployment(config policy.DeploymentPolicyConfig) {
	e.deployment = config
}

func (e *Engine) Deployment() policy.DeploymentPolicyConfig {
	return e.deployment
}

func (e *Engine) GatewayCaps() *GatewayCapabilities {
	return &e.gatewayCaps
}

func (e *Engine) GatewayLacksModel(key string) bool {
	if key == "" {
		return false
	}
	_, ok := e.unservedGatewayModels.Load(key)
	return ok
}

func (e *Engine) RememberGatewayLacksModel(key string) {
	e.unservedGatewayModels.Store(key, struct{}{})
}

func (e *Engine) SetRetrySleep(sleep func(context.Context, time.Duration) error) {
	e.retrySleep = sleep
}

func (e *Engine) RetrySleep() func(context.Context, time.Duration) error {
	return e.retrySleep
}
