// Command feedback_state_check exercises durable feedback against a migrated disposable Postgres.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"time"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/postgres"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/proxy"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/policy"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/sync/errgroup"
)

const checkModel = "claude-sonnet-4-6"

func main() {
	dsn := os.Getenv("ROUTER_TEST_DATABASE_URL")
	if dsn == "" {
		slog.Error("ROUTER_TEST_DATABASE_URL must name a disposable migrated database")
		os.Exit(1)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if err := check(ctx, dsn); err != nil {
		slog.Error("Feedback database check failed", "err", err)
		os.Exit(1)
	}
	slog.Info("Feedback database checks passed: completion order, idempotency, atomic rollback, scope isolation, lease recovery, permission and cleanup")
}

func check(ctx context.Context, dsn string) (checkErr error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return err
	}
	defer pool.Close()
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return err
	}
	readerName := "feedback-check-" + uuid.NewString()
	config.ConnConfig.RuntimeParams["application_name"] = readerName
	config.MaxConns = 1
	other, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return err
	}
	defer other.Close()
	repos := postgres.NewRepository(pool, auth.NoOpEncryptor{})
	installation, err := repos.Installations.Create(ctx, auth.CreateInstallationParams{ExternalID: uuid.NewString(), Name: "Feedback integration check"})
	if err != nil {
		return err
	}
	fixture := postgres.NewFeedbackCheckFixture(pool, *installation)
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		checkErr = errors.Join(checkErr, fixture.Cleanup(cleanupCtx))
	}()
	store, reader := postgres.NewRouterFeedbackRepo(pool), postgres.NewRouterFeedbackRepo(other)
	readerPID, err := postgres.NewFeedbackCheckFixture(other, *installation).BackendPID(ctx)
	if err != nil {
		return err
	}
	key := uuid.New()
	scope := key[:]
	request := func(id string) proxy.FeedbackRequest {
		return proxy.FeedbackRequest{InstallationID: installation.ID, SessionKey: scope, Role: "default_mid", RequestID: id, ServedModel: checkModel, ServedProvider: providers.ProviderAnthropic, Strategy: string(router.StrategyRL), RouteID: "route-" + id, TrainingAllowed: true}
	}
	event := func(selector int) proxy.RouterFeedbackEvent {
		return proxy.RouterFeedbackEvent{ID: uuid.NewString(), InstallationID: installation.ID, ExternalID: installation.ExternalID, SessionKey: scope, Role: "default_mid", Sequence: selector, RequestedModel: checkModel, Feedback: "useful answer", Rating: "up", Source: proxy.RouterFeedbackSourceUser, TrainingAllowed: true}
	}
	started := time.Now()
	if err := store.CompleteFeedbackRequest(ctx, request("A")); err != nil {
		return err
	}
	slog.Info("Feedback completion transaction measured", "latency_us", time.Since(started).Microseconds())
	if err := reader.CompleteFeedbackRequest(ctx, request("A")); err != nil {
		return err
	}
	type acceptance struct {
		event proxy.RouterFeedbackEvent
		err   error
	}
	accepted := make(chan acceptance, 1)
	err = fixture.HoldCompletion(ctx, request("B"), func() error {
		go func() { e, err := reader.AcceptRouterFeedback(ctx, event(-1)); accepted <- acceptance{e, err} }()
		ticker := time.NewTicker(10 * time.Millisecond)
		defer ticker.Stop()
		for {
			n, err := fixture.BlockedReaders(ctx, readerPID)
			if err != nil {
				return err
			}
			if n > 0 {
				return nil
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-ticker.C:
			}
		}
	})
	if err != nil {
		return err
	}
	result := <-accepted
	if result.err != nil {
		return result.err
	}
	if result.event.RequestID != "B" || result.event.TargetSequence != 2 {
		return fmt.Errorf("waiting acceptance chose %+v", result.event)
	}
	// All delayed-telemetry regressions exercise real history with no telemetry rows at all.
	for _, selector := range []int{0, -1, 2} {
		e, err := reader.AcceptRouterFeedback(ctx, event(selector))
		if err != nil {
			return err
		}
		if e.RequestID != "B" {
			return fmt.Errorf("selector %d chose %s instead of B", selector, e.RequestID)
		}
	}
	rollbackCompletion := errors.New("abort completion before commit")
	if err := fixture.HoldCompletion(ctx, request("aborted"), func() error { return rollbackCompletion }); !errors.Is(err, rollbackCompletion) {
		return fmt.Errorf("completion rollback: %v", err)
	}
	if err := store.CompleteFeedbackRequest(ctx, request("C")); err != nil {
		return err
	}
	selected, err := reader.AcceptRouterFeedback(ctx, event(-2))
	if err != nil {
		return err
	}
	if selected.RequestID != "B" {
		return errors.New("historical selector did not choose B")
	}
	unavailableInput := event(99)
	unavailable, err := store.AcceptRouterFeedback(ctx, unavailableInput)
	if err != nil {
		return err
	}
	if unavailable.Attached() {
		return errors.New("out-of-range feedback attached")
	}
	if err := store.CompleteFeedbackRequest(ctx, request("D")); err != nil {
		return err
	}
	selectedAgain, err := reader.AcceptRouterFeedback(ctx, selected)
	if err != nil {
		return err
	}
	if selectedAgain.RequestID != "B" {
		return errors.New("later completion retargeted accepted feedback")
	}
	unavailable, err = reader.AcceptRouterFeedback(ctx, unavailableInput)
	if err != nil {
		return err
	}
	if unavailable.Attached() {
		return errors.New("unavailable feedback was resolved again")
	}
	// A signed-link edit must survive both repeated acceptance and note-only commands.
	if err := repos.Feedback.Upsert(ctx, proxy.UpsertFeedbackParams{InstallationID: installation.ID, ExternalID: installation.ExternalID, RequestID: "B", Rating: "down", Source: "link"}); err != nil {
		return err
	}
	if _, err := reader.AcceptRouterFeedback(ctx, selected); err != nil {
		return err
	}
	note := event(2)
	note.Rating = ""
	note.Feedback = "a later note"
	if _, err := store.AcceptRouterFeedback(ctx, note); err != nil {
		return err
	}
	rating, err := repos.Feedback.GetContext(ctx, installation.ID, "B")
	if err != nil {
		return err
	}
	if rating.Rating != "down" {
		return errors.New("repeat or note overwrote signed-link rating")
	}
	rollback := event(2)
	err = fixture.HoldRating(ctx, "B", func() error {
		writeCtx, cancel := context.WithTimeout(ctx, 250*time.Millisecond)
		defer cancel()
		_, err := reader.AcceptRouterFeedback(writeCtx, rollback)
		if err == nil {
			return errors.New("acceptance did not block behind rating lock")
		}
		return nil
	})
	if err != nil {
		return err
	}
	if _, found, err := fixture.Command(ctx, rollback.ID); err != nil {
		return err
	} else if found {
		return errors.New("command survived rolled-back rating write")
	}
	rating, err = repos.Feedback.GetContext(ctx, installation.ID, "B")
	if err != nil {
		return err
	}
	if rating.Rating != "down" {
		return errors.New("failed acceptance changed rating")
	}
	// Installation/request uniqueness cannot leave a hole in a competing scope.
	var races errgroup.Group
	for i := range 8 {
		races.Go(func() error {
			p := request("duplicate")
			if i%2 == 1 {
				p.Role = "default_high"
			}
			if i%2 == 0 {
				return store.CompleteFeedbackRequest(ctx, p)
			}
			return reader.CompleteFeedbackRequest(ctx, p)
		})
	}
	if err := races.Wait(); err != nil {
		return err
	}
	low, err := fixture.History(ctx, scope, "default_mid")
	if err != nil {
		return err
	}
	high, err := fixture.History(ctx, scope, "default_high")
	if err != nil {
		return err
	}
	if len(low)+len(high) != 5 {
		return errors.New("concurrent completion duplicated request")
	}
	for _, rows := range [][]proxy.FeedbackRequest{low, high} {
		for i, row := range rows {
			if row.Sequence != int64(i+1) {
				return errors.New("completion history has a numbering hole")
			}
		}
	}
	for _, variant := range []string{"session", "role"} {
		p := event(-1)
		if variant == "session" {
			otherKey := uuid.New()
			p.SessionKey = otherKey[:]
		} else {
			p.Role = "default_low"
		}
		e, err := store.AcceptRouterFeedback(ctx, p)
		if err != nil {
			return err
		}
		if e.Attached() {
			return fmt.Errorf("history leaked across %s", variant)
		}
	}
	otherInstallation, err := repos.Installations.Create(ctx, auth.CreateInstallationParams{ExternalID: uuid.NewString(), Name: "Feedback isolation check"})
	if err != nil {
		return err
	}
	otherFixture := postgres.NewFeedbackCheckFixture(pool, *otherInstallation)
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		checkErr = errors.Join(checkErr, otherFixture.Cleanup(cleanupCtx))
	}()
	isolated := event(2)
	isolated.InstallationID = otherInstallation.ID
	isolated.ExternalID = otherInstallation.ExternalID
	isolatedResult, err := store.AcceptRouterFeedback(ctx, isolated)
	if err != nil {
		return err
	}
	if isolatedResult.Attached() {
		return errors.New("history leaked across installations")
	}
	// Competing replicas cannot claim the same unexpired event.
	claims := make([]proxy.RouterFeedbackEvent, 2)
	var group errgroup.Group
	for i := range claims {
		group.Go(func() error {
			claimer := store
			if i == 1 {
				claimer = reader
			}
			e, ok, err := claimer.ClaimRouterFeedback(ctx, uuid.NewString(), 30*time.Second)
			if err != nil {
				return err
			}
			if !ok {
				return errors.New("no pending claim")
			}
			claims[i] = e
			return nil
		})
	}
	if err := group.Wait(); err != nil {
		return err
	}
	if claims[0].ID == claims[1].ID {
		return errors.New("two replicas acquired the same lease")
	}
	if err := store.FinishRouterFeedback(ctx, claims[0].ID, uuid.NewString(), proxy.RouterFeedbackDelivered, "", time.Time{}); !errors.Is(err, proxy.ErrFeedbackLeaseLost) {
		return fmt.Errorf("wrong token settled claim: %v", err)
	}
	if err := fixture.MakeDue(ctx); err != nil {
		return err
	}
	recovered, ok, err := reader.ClaimRouterFeedback(ctx, uuid.NewString(), 30*time.Second)
	if err != nil {
		return err
	}
	if !ok || recovered.ID != claims[0].ID && recovered.ID != claims[1].ID {
		return errors.New("fresh repository did not recover expired work")
	}
	var oldToken string
	for _, c := range claims {
		if c.ID == recovered.ID {
			oldToken = c.LeaseToken
		}
	}
	if err := store.FinishRouterFeedback(ctx, recovered.ID, oldToken, proxy.RouterFeedbackDelivered, "", time.Time{}); !errors.Is(err, proxy.ErrFeedbackLeaseLost) {
		return fmt.Errorf("stale owner settled recovered claim: %v", err)
	}
	// Simulate remote success followed by process death; retry must use the same event ID.
	receiver := &deduplicatingReceiver{effects: make(map[string]int), attempts: make(map[string]int)}
	if err := receiver.ReportFeedback(ctx, map[string]interface{}{"feedback_id": recovered.ID}); err != nil {
		return err
	}
	if err := fixture.MakeDue(ctx); err != nil {
		return err
	}
	if err := fixture.SetTrainingAllowed(ctx, true); err != nil {
		return err
	}
	svc := proxy.NewService(nil, nil, nil, false, nil, nil, false, "", "", nil).WithPolicyStrategy(policy.StrategySpec{Strategy: router.StrategyRL, Router: receiver, FeedbackRetrySafe: true})
	for {
		claimed, err := svc.ProcessRouterFeedback(ctx, reader)
		if err != nil {
			return err
		}
		if !claimed {
			break
		}
	}
	// The drain also delivers the other pending commands once each; only the
	// recovered one must arrive a second time under its original identity.
	if receiver.attempts[recovered.ID] != 2 {
		return fmt.Errorf("retry delivered recovered command %d times, want the seeded success plus one redelivery", receiver.attempts[recovered.ID])
	}
	if receiver.effects[recovered.ID] != 1 {
		return errors.New("redelivery repeated remote effect")
	}
	for id, n := range receiver.attempts {
		if id != recovered.ID && n != 1 {
			return fmt.Errorf("command %s was delivered %d times without a lost lease", id, n)
		}
	}
	rating, err = repos.Feedback.GetContext(ctx, installation.ID, "B")
	if err != nil {
		return err
	}
	if rating.Rating != "down" {
		return errors.New("worker overwrote local rating")
	}
	if err := fixture.SetTrainingAllowed(ctx, false); err != nil {
		return err
	}
	allowed, err := reader.RouterFeedbackTrainingAllowed(ctx, installation.ID, installation.ExternalID)
	if err != nil {
		return err
	}
	if allowed {
		return errors.New("permission revocation was not observed")
	}
	return nil
}

// deduplicatingReceiver models a learner that keys its effect on feedback_id:
// attempts counts every delivery, effects only the first per id.
type deduplicatingReceiver struct {
	mu       sync.Mutex
	effects  map[string]int
	attempts map[string]int
}

func (*deduplicatingReceiver) Route(context.Context, router.Request) (router.Decision, error) {
	return router.Decision{}, errors.New("fixture cannot route")
}
func (r *deduplicatingReceiver) ReportFeedback(_ context.Context, p map[string]interface{}) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	id, ok := p["feedback_id"].(string)
	if !ok || id == "" {
		return errors.New("missing feedback id")
	}
	r.attempts[id]++
	if _, exists := r.effects[id]; !exists {
		r.effects[id] = 1
	}
	return nil
}
