package billing

import (
	"context"
	"errors"
	"fmt"
)

type OwnerKind string

const (
	OwnerKindOrganization OwnerKind = "organization"
	OwnerKindSubscriber   OwnerKind = "subscriber"
	OwnerKindInternalTest OwnerKind = "internal_test"
)

type Owner struct {
	Kind           OwnerKind
	OrganizationID string
	SubscriberID   string
	TestSubjectID  string
}

func OrganizationOwner(organizationID string) Owner {
	return Owner{Kind: OwnerKindOrganization, OrganizationID: organizationID}
}

func SubscriberOwner(subscriberID string) Owner {
	return Owner{Kind: OwnerKindSubscriber, SubscriberID: subscriberID}
}

func (o Owner) Validate() error {
	switch o.Kind {
	case OwnerKindInternalTest:
		if o.TestSubjectID == "" || o.SubscriberID != "" || o.OrganizationID != "" {
			return fmt.Errorf("%w: internal test owner needs only a test subject", ErrInvalidOwner)
		}
		return nil
	case OwnerKindOrganization:
		if o.OrganizationID == "" || o.SubscriberID != "" || o.TestSubjectID != "" {
			return fmt.Errorf("%w: organization owner needs only an organization id", ErrInvalidOwner)
		}
		return nil
	case OwnerKindSubscriber:
		if o.SubscriberID == "" || o.OrganizationID != "" || o.TestSubjectID != "" {
			return fmt.Errorf("%w: subscriber owner needs only a subscriber id", ErrInvalidOwner)
		}
		return nil
	default:
		return fmt.Errorf("%w: %q", ErrInvalidOwner, o.Kind)
	}
}

var ErrInvalidOwner = errors.New("billing: invalid prepaid owner")

var ErrOwnerKindUnsupported = errors.New("billing: prepaid owner kind unsupported")

type PrepaidDebit struct {
	Owner              Owner
	DeltaUsdMicros     int64
	NotionalCostMicros int64
	EntryType          string
	FeeUsdMicros       int64
	FeeEntryType       string
	RouterRequestID    string
	RouterModel        string
	APIKeyID           string
	RouterUserID       string
	DelegatedSessionID string
	DelegatedTaskID    string
}

type PrepaidBook interface {
	Balance(ctx context.Context, owner Owner) (balanceMicros int64, err error)
	Debit(ctx context.Context, debit PrepaidDebit) (balanceAfterMicros int64, err error)
}

type PrepaidBooks struct {
	books map[OwnerKind]PrepaidBook
}

func newPrepaidBooks() *PrepaidBooks {
	return &PrepaidBooks{books: make(map[OwnerKind]PrepaidBook, 2)}
}

func (b *PrepaidBooks) register(kind OwnerKind, book PrepaidBook) {
	b.books[kind] = book
}

func (b *PrepaidBooks) bookFor(owner Owner) (PrepaidBook, error) {
	if err := owner.Validate(); err != nil {
		return nil, err
	}
	book, ok := b.books[owner.Kind]
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrOwnerKindUnsupported, owner.Kind)
	}
	return book, nil
}

func (b *PrepaidBooks) Balance(ctx context.Context, owner Owner) (int64, error) {
	book, err := b.bookFor(owner)
	if err != nil {
		return 0, err
	}
	return book.Balance(ctx, owner)
}

func (b *PrepaidBooks) Debit(ctx context.Context, debit PrepaidDebit) (int64, error) {
	book, err := b.bookFor(debit.Owner)
	if err != nil {
		return 0, err
	}
	return book.Debit(ctx, debit)
}

type organizationBook struct {
	repo Repo
}

func OrganizationBook(repo Repo) PrepaidBook {
	return organizationBook{repo: repo}
}

func (b organizationBook) Balance(ctx context.Context, owner Owner) (int64, error) {
	if err := owner.Validate(); err != nil {
		return 0, err
	}
	if owner.Kind != OwnerKindOrganization {
		return 0, fmt.Errorf("%w: organization book cannot read %q funds", ErrOwnerKindUnsupported, owner.Kind)
	}
	return b.repo.GetBalance(ctx, owner.OrganizationID)
}

func (b organizationBook) Debit(ctx context.Context, debit PrepaidDebit) (int64, error) {
	if err := debit.Owner.Validate(); err != nil {
		return 0, err
	}
	if debit.Owner.Kind != OwnerKindOrganization {
		return 0, fmt.Errorf("%w: organization book cannot debit %q funds", ErrOwnerKindUnsupported, debit.Owner.Kind)
	}
	return b.repo.DebitInference(ctx, DebitParams{
		OrganizationID:     debit.Owner.OrganizationID,
		DeltaUsdMicros:     debit.DeltaUsdMicros,
		NotionalCostMicros: debit.NotionalCostMicros,
		EntryType:          debit.EntryType,
		FeeUsdMicros:       debit.FeeUsdMicros,
		FeeEntryType:       debit.FeeEntryType,
		RouterRequestID:    debit.RouterRequestID,
		RouterModel:        debit.RouterModel,
		APIKeyID:           debit.APIKeyID,
		RouterUserID:       debit.RouterUserID,
		DelegatedSessionID: debit.DelegatedSessionID,
		DelegatedTaskID:    debit.DelegatedTaskID,
	})
}
