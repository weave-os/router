package billing

import (
	"context"
	"errors"
	"weave-os/router/internal/requestcontext"
)

// InternalTestOwner addresses only a subject-owned internal test book.
func InternalTestOwner(subjectID string) Owner {
	return Owner{Kind: OwnerKindInternalTest, TestSubjectID: subjectID}
}

// WithInternalTestPrepaid registers the isolated book without customer funding fallback.
func (s *Service) WithInternalTestPrepaid(book PrepaidBook) *Service {
	s.prepaid.register(OwnerKindInternalTest, book)
	return s
}
func (s *Service) debitInternalTest(ctx context.Context, p DebitInferenceParams, subjectID string, cost int64) (int64, error) {
	if p.HasOverride || p.SubscriptionServed || p.ByokServed {
		return 0, errors.New("internal test inference requires isolated prepaid funding")
	}
	return s.DebitPrepaid(ctx, PrepaidDebit{Owner: InternalTestOwner(subjectID), DeltaUsdMicros: -cost, NotionalCostMicros: cost, EntryType: EntryTypeInference, RouterRequestID: p.RouterRequestID, RouterModel: p.Model, APIKeyID: p.APIKeyID})
}

// InternalTestOwnerFrom derives billing ownership from verified request identity.
func InternalTestOwnerFrom(ctx context.Context) (Owner, bool) {
	identity, ok := requestcontext.InternalTestIdentityFrom(ctx)
	return InternalTestOwner(identity.SubjectID), ok
}
