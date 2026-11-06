package pubsub

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"cloud.google.com/go/pubsub/v2/apiv1/pubsubpb"
	"github.com/googleapis/gax-go/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type fakeSubscriptionAdmin struct {
	mu          sync.Mutex
	createErr   []error
	stallFirstN int
	created     []string
	deleted     []string
}

func (f *fakeSubscriptionAdmin) CreateSubscription(ctx context.Context, req *pubsubpb.Subscription, _ ...gax.CallOption) (*pubsubpb.Subscription, error) {
	f.mu.Lock()
	f.created = append(f.created, req.GetName())
	stall := len(f.created) <= f.stallFirstN
	var err error
	if !stall && len(f.createErr) > 0 {
		err = f.createErr[0]
		f.createErr = f.createErr[1:]
	}
	f.mu.Unlock()
	if stall {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if err != nil {
		return nil, err
	}
	return req, nil
}

func (f *fakeSubscriptionAdmin) DeleteSubscription(_ context.Context, req *pubsubpb.DeleteSubscriptionRequest, _ ...gax.CallOption) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deleted = append(f.deleted, req.GetSubscription())
	return nil
}

var fastReplicaRetry = replicaSubscriptionRetry{attemptTimeout: time.Second, backoff: gax.Backoff{Initial: time.Millisecond, Max: time.Millisecond}}

func TestCreateReplicaSubscription_RetriesTransientFailuresUnderOneName(t *testing.T) {
	admin := &fakeSubscriptionAdmin{createErr: []error{
		context.DeadlineExceeded,
		status.Error(codes.Unavailable, "backend unavailable"),
	}}

	name, cleanup, err := createReplicaSubscription(context.Background(), admin, fastReplicaRetry, "proj", "topic", "prefix-")

	require.NoError(t, err)
	assert.Equal(t, []string{name, name, name}, admin.created)
	assert.Regexp(t, `^projects/proj/subscriptions/prefix-[0-9a-f-]{36}$`, name)
	cleanup()
	assert.Equal(t, []string{name}, admin.deleted)
}

func TestCreateReplicaSubscription_AlreadyExistsOnRetryMeansEarlierAttemptCreatedIt(t *testing.T) {
	admin := &fakeSubscriptionAdmin{createErr: []error{
		status.Error(codes.DeadlineExceeded, "deadline exceeded"),
		status.Error(codes.AlreadyExists, "already exists"),
	}}

	name, _, err := createReplicaSubscription(context.Background(), admin, fastReplicaRetry, "proj", "topic", "prefix")

	require.NoError(t, err)
	assert.Len(t, admin.created, 2)
	assert.Equal(t, admin.created[0], name)
}

func TestCreateReplicaSubscription_PermanentFailureIsNotRetried(t *testing.T) {
	admin := &fakeSubscriptionAdmin{createErr: []error{status.Error(codes.PermissionDenied, "denied")}}

	_, _, err := createReplicaSubscription(context.Background(), admin, fastReplicaRetry, "proj", "topic", "prefix")

	require.Error(t, err)
	assert.Equal(t, codes.PermissionDenied, status.Code(errors.Unwrap(err)))
	assert.Len(t, admin.created, 1)
}

func TestCreateReplicaSubscription_AlreadyExistsOnFirstAttemptMeansClientRetryCreatedIt(t *testing.T) {
	admin := &fakeSubscriptionAdmin{createErr: []error{status.Error(codes.AlreadyExists, "already exists")}}

	name, _, err := createReplicaSubscription(context.Background(), admin, fastReplicaRetry, "proj", "topic", "prefix")

	require.NoError(t, err)
	assert.Equal(t, []string{name}, admin.created)
}

func TestCreateReplicaSubscription_StalledAttemptIsCutOffAndRetried(t *testing.T) {
	admin := &fakeSubscriptionAdmin{stallFirstN: 1}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	retry := replicaSubscriptionRetry{attemptTimeout: 20 * time.Millisecond, backoff: fastReplicaRetry.backoff}

	name, _, err := createReplicaSubscription(ctx, admin, retry, "proj", "topic", "prefix")

	require.NoError(t, err)
	assert.Equal(t, []string{name, name}, admin.created)
}

func TestCreateReplicaSubscription_StopsWhenCallerBudgetExpires(t *testing.T) {
	admin := &fakeSubscriptionAdmin{}
	for range 1000 {
		admin.createErr = append(admin.createErr, status.Error(codes.Unavailable, "unavailable"))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	_, _, err := createReplicaSubscription(ctx, admin, replicaSubscriptionRetry{attemptTimeout: time.Second, backoff: gax.Backoff{Initial: 10 * time.Millisecond, Max: 10 * time.Millisecond}}, "proj", "topic", "prefix")

	require.Error(t, err)
	assert.Equal(t, codes.Unavailable, status.Code(errors.Unwrap(err)))
	assert.Less(t, len(admin.created), 1000)
}

func TestCreateReplicaSubscription_RequiresPrefix(t *testing.T) {
	admin := &fakeSubscriptionAdmin{}

	_, _, err := createReplicaSubscription(context.Background(), admin, fastReplicaRetry, "proj", "topic", "")

	require.Error(t, err)
	assert.Empty(t, admin.created)
}
