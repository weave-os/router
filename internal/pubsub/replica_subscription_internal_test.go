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
	mu        sync.Mutex
	createErr []error
	created   []string
	deleted   []string
}

func (f *fakeSubscriptionAdmin) CreateSubscription(_ context.Context, req *pubsubpb.Subscription, _ ...gax.CallOption) (*pubsubpb.Subscription, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.created = append(f.created, req.GetName())
	if len(f.createErr) == 0 {
		return req, nil
	}
	err := f.createErr[0]
	f.createErr = f.createErr[1:]
	return nil, err
}

func (f *fakeSubscriptionAdmin) DeleteSubscription(_ context.Context, req *pubsubpb.DeleteSubscriptionRequest, _ ...gax.CallOption) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deleted = append(f.deleted, req.GetSubscription())
	return nil
}

var fastReplicaRetry = replicaSubscriptionRetry{attemptTimeout: time.Second, backoff: time.Millisecond}

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

func TestCreateReplicaSubscription_AlreadyExistsOnFirstAttemptIsAnError(t *testing.T) {
	admin := &fakeSubscriptionAdmin{createErr: []error{status.Error(codes.AlreadyExists, "already exists")}}

	_, _, err := createReplicaSubscription(context.Background(), admin, fastReplicaRetry, "proj", "topic", "prefix")

	require.Error(t, err)
	assert.Len(t, admin.created, 1)
}

func TestCreateReplicaSubscription_StopsWhenCallerBudgetExpires(t *testing.T) {
	admin := &fakeSubscriptionAdmin{}
	for range 1000 {
		admin.createErr = append(admin.createErr, status.Error(codes.Unavailable, "unavailable"))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	_, _, err := createReplicaSubscription(ctx, admin, replicaSubscriptionRetry{attemptTimeout: time.Second, backoff: 10 * time.Millisecond}, "proj", "topic", "prefix")

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
