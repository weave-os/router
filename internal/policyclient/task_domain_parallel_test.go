package policyclient

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"weave-os/router/internal/router/policy"
	"weave-os/router/internal/router/taskdomain"
)

type taskResolverFunc func(context.Context, taskdomain.Input) taskdomain.Outcome

func (f taskResolverFunc) Resolve(ctx context.Context, input taskdomain.Input) taskdomain.Outcome {
	return f(ctx, input)
}

const taskTestComplexity = `{"schema_version":"policy_router_v3","predicted_label":"low","class_probabilities":{"low":1},"ranked_fallback":[{"group":"low","probability":1}]}`

func TestTaskAndComplexityStartTogetherAndJoin(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	complexityStarted, taskStarted, releaseTask := make(chan struct{}), make(chan struct{}), make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(complexityStarted)
		select {
		case <-taskStarted:
		case <-r.Context().Done():
			return
		}
		_, _ = w.Write([]byte(taskTestComplexity))
	}))
	defer server.Close()
	client := New(server.URL, server.Client(), 0, WithTaskDomain(taskResolverFunc(func(ctx context.Context, _ taskdomain.Input) taskdomain.Outcome {
		close(taskStarted)
		select {
		case <-releaseTask:
			return taskdomain.Outcome{Status: taskdomain.Ready}
		case <-ctx.Done():
			return taskdomain.Outcome{Status: taskdomain.TimedOut}
		}
	})))
	type completion struct {
		result policy.Result
		err    error
	}
	done := make(chan completion, 1)
	go func() {
		result, err := client.Decide(ctx, policy.Query{ExecutionMode: policy.ExecutionModeServing, TaskDomain: &taskdomain.Input{UserText: "task"}})
		done <- completion{result, err}
	}()
	select {
	case <-complexityStarted:
	case <-ctx.Done():
		t.Fatal("complexity did not start")
	}
	select {
	case <-taskStarted:
	case <-ctx.Done():
		t.Fatal("task did not start")
	}
	select {
	case <-done:
		t.Fatal("returned before task completion")
	default:
	}
	close(releaseTask)
	select {
	case completed := <-done:
		require.NoError(t, completed.err)
		require.NotNil(t, completed.result.TaskDomain)
		assert.Equal(t, taskdomain.Ready, completed.result.TaskDomain.Status)
		assert.Equal(t, map[string]float64{"low": 1}, completed.result.ClassProbabilities)
	case <-ctx.Done():
		t.Fatal("join did not complete")
	}
}

func TestTaskJoinPreservesComplexityErrorsAndCancellation(t *testing.T) {
	for _, parentCancelled := range []bool{false, true} {
		t.Run(map[bool]string{false: "complexity error", true: "parent cancellation"}[parentCancelled], func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "unavailable", 503) }))
			defer server.Close()
			client := New(server.URL, server.Client(), 0, WithTaskDomain(taskResolverFunc(func(ctx context.Context, _ taskdomain.Input) taskdomain.Outcome {
				<-ctx.Done()
				return taskdomain.Outcome{Status: taskdomain.Unavailable}
			})))
			if parentCancelled {
				cancel()
			}
			_, err := client.Decide(ctx, policy.Query{ExecutionMode: policy.ExecutionModeServing, TaskDomain: &taskdomain.Input{}})
			require.Error(t, err)
			if parentCancelled {
				assert.True(t, errors.Is(err, context.Canceled))
			}
		})
	}
}

func TestOptionalTaskFailureRetainsComplexity(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(taskTestComplexity)) }))
	defer server.Close()
	client := New(server.URL, server.Client(), 0, WithTaskDomain(taskResolverFunc(func(context.Context, taskdomain.Input) taskdomain.Outcome {
		return taskdomain.Outcome{Status: taskdomain.TimedOut}
	})))
	result, err := client.Decide(context.Background(), policy.Query{ExecutionMode: policy.ExecutionModeServing, TaskDomain: &taskdomain.Input{}})
	require.NoError(t, err)
	assert.Equal(t, "low", result.PredictedLabel)
	require.NotNil(t, result.TaskDomain)
	assert.Equal(t, taskdomain.TimedOut, result.TaskDomain.Status)
}
