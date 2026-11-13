package trafficcapture

import (
	"context"
	"testing"
)

type recordingSink struct{}

func (recordingSink) Record(Exchange) error { return nil }

func TestWithRecorderScopesSequentialUpstreamAttempts(t *testing.T) {
	ctx := WithRecorder(context.Background(), recordingSink{}, "inbound-1")
	scope := FromContext(ctx)
	if scope == nil {
		t.Fatal("expected capture scope in context")
	}
	if scope.ExchangeID() != "inbound-1" {
		t.Fatalf("exchange ID = %q, want inbound-1", scope.ExchangeID())
	}
	if scope.Recorder() == nil {
		t.Fatal("expected recorder on capture scope")
	}

	firstID, firstAttempt := scope.NextAttempt()
	secondID, secondAttempt := scope.NextAttempt()
	if firstID != "inbound-1-1" || firstAttempt != 1 {
		t.Fatalf("first attempt = (%q, %d), want (inbound-1-1, 1)", firstID, firstAttempt)
	}
	if secondID != "inbound-1-2" || secondAttempt != 2 {
		t.Fatalf("second attempt = (%q, %d), want (inbound-1-2, 2)", secondID, secondAttempt)
	}
}

func TestFromContextWithoutRecorder(t *testing.T) {
	if scope := FromContext(context.Background()); scope != nil {
		t.Fatalf("scope = %#v, want nil", scope)
	}
}

func TestNewIDIsUnique(t *testing.T) {
	firstID := NewID()
	secondID := NewID()
	if firstID == "" || secondID == "" || firstID == secondID {
		t.Fatalf("IDs = %q and %q, want two distinct non-empty values", firstID, secondID)
	}
}
