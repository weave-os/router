package billing

import (
	"context"
	"errors"
	"fmt"
)

// OwnerKind names the party a prepaid book belongs to. Organization funds are
// the Enterprise balance every managed org has had; subscriber funds belong to
// one individual Max/Boost plan holder and are keyed by the stable credential
// subject rather than by any organization the subscriber happens to work in.
type OwnerKind string

const (
	OwnerKindOrganization OwnerKind = "organization"
	OwnerKindSubscriber   OwnerKind = "subscriber"
)

// Owner identifies whose prepaid money a read or debit touches. The two id
// fields are deliberately separate: a subscriber id must never travel as an
// organization id, since that would spend organization funds on an individual
// plan (and vice versa).
type Owner struct {
	Kind           OwnerKind
	OrganizationID string
	SubscriberID   string
}

// OrganizationOwner addresses an organization's prepaid balance.
func OrganizationOwner(organizationID string) Owner {
	return Owner{Kind: OwnerKindOrganization, OrganizationID: organizationID}
}

// SubscriberOwner addresses one individual subscriber's prepaid balance,
// identified by their credential-subject id.
func SubscriberOwner(subscriberID string) Owner {
	return Owner{Kind: OwnerKindSubscriber, SubscriberID: subscriberID}
}

// Validate rejects an owner whose kind and identifier disagree, so a
// half-populated owner fails before it reaches a book that would read the
// field it does carry.
func (o Owner) Validate() error {
	switch o.Kind {
	case OwnerKindOrganization:
		if o.OrganizationID == "" || o.SubscriberID != "" {
			return fmt.Errorf("%w: organization owner needs only an organization id", ErrInvalidOwner)
		}
		return nil
	case OwnerKindSubscriber:
		if o.SubscriberID == "" || o.OrganizationID != "" {
			return fmt.Errorf("%w: subscriber owner needs only a subscriber id", ErrInvalidOwner)
		}
		return nil
	default:
		return fmt.Errorf("%w: %q", ErrInvalidOwner, o.Kind)
	}
}

// ErrInvalidOwner marks an owner that names no kind, no identifier, or an
// identifier belonging to a different kind.
var ErrInvalidOwner = errors.New("billing: invalid prepaid owner")

// ErrOwnerKindUnsupported is returned when no book is wired for an owner kind
// — for example a subscriber debit on a deployment that has no individual
// subscriptions. Callers treat it as "this owner cannot pay here", not as a
// database failure.
var ErrOwnerKindUnsupported = errors.New("billing: prepaid owner kind unsupported")

// PrepaidDebit is one debit against a single owner's prepaid book.
type PrepaidDebit struct {
	Owner Owner
	// DeltaUsdMicros is signed: negative for a real charge, zero for a
	// pass-through that still records notional cost.
	DeltaUsdMicros     int64
	NotionalCostMicros int64
	EntryType          string
	// FeeUsdMicros writes a second row of FeeEntryType in the same statement
	// when non-zero; only the organization book charges BYOK fees today.
	FeeUsdMicros    int64
	FeeEntryType    string
	RouterRequestID string
	RouterModel     string
	// APIKeyID and RouterUserID attribute an organization debit to the key and
	// engineer that spent it. An individual plan has no such fan-out.
	APIKeyID     string
	RouterUserID string
}

// PrepaidBook is one owner kind's prepaid money: balance reads and atomic
// debit-with-ledger. Implementations must scope every statement to the owner
// they are handed.
type PrepaidBook interface {
	Balance(ctx context.Context, owner Owner) (balanceMicros int64, err error)
	Debit(ctx context.Context, debit PrepaidDebit) (balanceAfterMicros int64, err error)
}

// PrepaidBooks routes a typed owner to the book that holds its money. Kinds
// without a registered book fail closed with ErrOwnerKindUnsupported rather
// than falling back to another owner's funds.
type PrepaidBooks struct {
	books map[OwnerKind]PrepaidBook
}

// newPrepaidBooks starts an empty router; the service registers the
// organization book at construction and subscriber funds are opted in.
func newPrepaidBooks() *PrepaidBooks {
	return &PrepaidBooks{books: make(map[OwnerKind]PrepaidBook, 2)}
}

// register binds a book to an owner kind, replacing any previous binding.
func (b *PrepaidBooks) register(kind OwnerKind, book PrepaidBook) {
	b.books[kind] = book
}

// bookFor resolves the book that owns this party's money.
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

// Balance reads the owner's prepaid balance from its own book.
func (b *PrepaidBooks) Balance(ctx context.Context, owner Owner) (int64, error) {
	book, err := b.bookFor(owner)
	if err != nil {
		return 0, err
	}
	return book.Balance(ctx, owner)
}

// Debit charges the owner's own book.
func (b *PrepaidBooks) Debit(ctx context.Context, debit PrepaidDebit) (int64, error) {
	book, err := b.bookFor(debit.Owner)
	if err != nil {
		return 0, err
	}
	return book.Debit(ctx, debit)
}

// organizationBook adapts the long-standing organization Repo to the
// owner-typed contract, so the Enterprise balance keeps its exact behavior
// (overrides, BYOK fee row, key/user/org spend counters) while flowing through
// the same entry point as subscriber funds.
type organizationBook struct {
	repo Repo
}

// OrganizationBook exposes the organization prepaid book over a billing Repo.
// It revalidates the owner itself, since it is usable directly and not only
// behind PrepaidBooks' dispatch.
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
	})
}
