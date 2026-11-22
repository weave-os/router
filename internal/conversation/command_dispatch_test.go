package conversation_test

import (
	"context"
	"errors"
	"testing"

	"weave-os/router/internal/conversation"
	"weave-os/router/internal/translate"
)

type fakeCommandSource struct {
	forceModel     *translate.ForceModelResult
	feedback       *translate.RouterFeedbackResult
	session        bool
	models         bool
	listModels     bool
	extractedForce bool
}

func (f *fakeCommandSource) ExtractForceModelCommand() (translate.ForceModelResult, bool) {
	f.extractedForce = true
	if f.forceModel == nil {
		return translate.ForceModelResult{}, false
	}
	return *f.forceModel, true
}

func (f *fakeCommandSource) ExtractRouterFeedbackCommand() (translate.RouterFeedbackResult, bool) {
	if f.feedback == nil {
		return translate.RouterFeedbackResult{}, false
	}
	return *f.feedback, true
}

func (f *fakeCommandSource) ExtractRouterSessionCommand() bool { return f.session }
func (f *fakeCommandSource) ExtractRouterModelsCommand() bool  { return f.models }
func (f *fakeCommandSource) ExtractListModelsCommand() bool    { return f.listModels }

type recordingCommandHandlers struct {
	calls []string
	err   error
}

func (r *recordingCommandHandlers) record(name string) error {
	r.calls = append(r.calls, name)
	return r.err
}

func (r *recordingCommandHandlers) ApplyForceModel(context.Context, translate.ForceModelResult) (string, error) {
	return "agent-model", r.record("apply-force-model")
}

func (r *recordingCommandHandlers) AcknowledgeForceModel(context.Context, translate.ForceModelResult) error {
	return r.record("ack-force-model")
}

func (r *recordingCommandHandlers) RecordRouterFeedback(_ context.Context, _ translate.RouterFeedbackResult, synthetic bool) error {
	if synthetic {
		return r.record("feedback-synthetic")
	}
	return r.record("feedback")
}

func (r *recordingCommandHandlers) RouterSession(context.Context) error { return r.record("session") }
func (r *recordingCommandHandlers) RouterModels(context.Context) error  { return r.record("models") }
func (r *recordingCommandHandlers) ListModels(context.Context) error    { return r.record("list-models") }
func (r *recordingCommandHandlers) GrantContinuation(context.Context) {
	r.calls = append(r.calls, "grant")
}

func dispatchFor(source *fakeCommandSource, handlers *recordingCommandHandlers) conversation.CommandDispatch {
	return conversation.CommandDispatch{
		Source:                  source,
		Handlers:                handlers,
		Protocol:                "test",
		ForceModelEnabled:       true,
		ForceModelContinuesTurn: func(cmd translate.ForceModelResult) bool { return cmd.FromToolResult },
		SyntheticFeedback:       func(cmd translate.RouterFeedbackResult) bool { return !cmd.FromToolResult },
	}
}

func TestCommandDispatchToolResultCommandsContinueTheTurn(t *testing.T) {
	source := &fakeCommandSource{
		forceModel: &translate.ForceModelResult{FromToolResult: true},
		feedback:   &translate.RouterFeedbackResult{FromToolResult: true},
		session:    true,
	}
	handlers := &recordingCommandHandlers{}
	out, err := dispatchFor(source, handlers).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := conversation.CommandOutcome{Handled: true, AgentForceModel: "agent-model", BodyChanged: true}
	if out != want {
		t.Fatalf("outcome = %+v, want %+v", out, want)
	}
	assertCalls(t, handlers.calls, "apply-force-model", "feedback", "session", "grant")
}

func TestCommandDispatchStandaloneCommandEndsTheTurn(t *testing.T) {
	source := &fakeCommandSource{forceModel: &translate.ForceModelResult{}, session: true}
	handlers := &recordingCommandHandlers{}
	out, err := dispatchFor(source, handlers).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if out != (conversation.CommandOutcome{Handled: true}) {
		t.Fatalf("outcome = %+v", out)
	}
	assertCalls(t, handlers.calls, "ack-force-model", "grant")
}

func TestCommandDispatchRouterInfoOrder(t *testing.T) {
	source := &fakeCommandSource{models: true, listModels: true}
	handlers := &recordingCommandHandlers{}
	if _, err := dispatchFor(source, handlers).Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	assertCalls(t, handlers.calls, "models", "grant")
}

func TestCommandDispatchSkipsForceModelWhenDisabled(t *testing.T) {
	source := &fakeCommandSource{forceModel: &translate.ForceModelResult{}}
	dispatch := dispatchFor(source, &recordingCommandHandlers{})
	dispatch.ForceModelEnabled = false
	out, err := dispatch.Run(context.Background())
	if err != nil || out != (conversation.CommandOutcome{}) || source.extractedForce {
		t.Fatalf("outcome = %+v, err = %v, extracted = %v", out, err, source.extractedForce)
	}
}

func TestCommandDispatchReturnsHandlerErrorWithoutGrant(t *testing.T) {
	boom := errors.New("boom")
	handlers := &recordingCommandHandlers{err: boom}
	_, err := dispatchFor(&fakeCommandSource{feedback: &translate.RouterFeedbackResult{}}, handlers).Run(context.Background())
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
	assertCalls(t, handlers.calls, "feedback-synthetic")
}

func assertCalls(t *testing.T, got []string, want ...string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("calls = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("calls = %v, want %v", got, want)
		}
	}
}
