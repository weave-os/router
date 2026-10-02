package health

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRequestCapacityRecoversOnlyBelowLowWatermark(t *testing.T) {
	capacity, err := NewCapacity(Limits{MaxRequests: 3, ResumeRequests: 1})
	require.NoError(t, err)
	first, second, third := capacity.TryAcquire(), capacity.TryAcquire(), capacity.TryAcquire()
	require.NotNil(t, first)
	require.NotNil(t, second)
	require.NotNil(t, third)
	require.Nil(t, capacity.TryAcquire())
	assert.Equal(t, ReasonRequests, capacity.Snapshot().Reason)
	first.Release()
	require.Nil(t, capacity.TryAcquire(), "one available slot must not flap readiness")
	second.Release()
	require.True(t, capacity.Snapshot().Ready)
	resumed := capacity.TryAcquire()
	require.NotNil(t, resumed)
	resumed.Release()
	third.Release()
	third.Release()
	assert.Zero(t, capacity.Snapshot().ActiveRequests)
}

func TestBufferReservationIncludesTransientCopiesAndReleasesOnCompletion(t *testing.T) {
	capacity, err := NewCapacity(Limits{MaxBufferedBytes: 1024, ResumeBufferedBytes: 512})
	require.NoError(t, err)
	first, second := capacity.TryAcquire(), capacity.TryAcquire()
	require.True(t, first.ResizeBufferedBytes(700))
	require.False(t, second.ResizeBufferedBytes(400))
	assert.EqualValues(t, 700, capacity.Snapshot().BufferedBytes)
	require.True(t, second.ResizeBufferedBytes(324))
	assert.False(t, capacity.Snapshot().Ready)
	second.Release()
	assert.False(t, capacity.Snapshot().Ready)
	require.True(t, first.ResizeBufferedBytes(500))
	assert.True(t, capacity.Snapshot().Ready)
	first.Release()
	assert.Zero(t, capacity.Snapshot().BufferedBytes)
	assert.False(t, first.ResizeBufferedBytes(5), "completed work cannot reacquire memory")
}

func TestMemoryPressureDoesNotRecoverOnUnknownMeasurement(t *testing.T) {
	capacity, err := NewCapacity(Limits{MemoryHighBytes: 900, MemoryLowBytes: 700})
	require.NoError(t, err)
	usage := uint64(950)
	capacity.UpdateResources(Resources{MemoryUsageBytes: &usage})
	assert.Equal(t, ReasonMemory, capacity.Snapshot().Reason)
	capacity.UpdateResources(Resources{Source: ResourceUnavailable})
	assert.False(t, capacity.Snapshot().Ready)
	assert.Nil(t, capacity.Snapshot().Resources.MemoryUsageBytes)
	usage = 800
	capacity.UpdateResources(Resources{MemoryUsageBytes: &usage})
	assert.False(t, capacity.Snapshot().Ready)
	usage = 700
	capacity.UpdateResources(Resources{MemoryUsageBytes: &usage})
	assert.True(t, capacity.Snapshot().Ready)
}

func TestShutdownRejectsNewWorkWithoutReleasingAcceptedWork(t *testing.T) {
	capacity, err := NewCapacity(Limits{MaxRequests: 2, ResumeRequests: 1})
	require.NoError(t, err)
	accepted := capacity.TryAcquire()
	require.NotNil(t, accepted)
	capacity.Shutdown()
	assert.Nil(t, capacity.TryAcquire())
	assert.Equal(t, ReasonShutdown, capacity.Snapshot().Reason)
	assert.Equal(t, 1, capacity.Snapshot().ActiveRequests)
	accepted.Release()
	assert.False(t, capacity.Snapshot().Ready)
	assert.Zero(t, capacity.Snapshot().ActiveRequests)
}

func TestConcurrentAdmissionCannotExceedBound(t *testing.T) {
	capacity, err := NewCapacity(Limits{MaxRequests: 4, ResumeRequests: 3})
	require.NoError(t, err)
	var attempts sync.WaitGroup
	accepted := make(chan *Permit, 64)
	for range 64 {
		attempts.Add(1)
		go func() {
			defer attempts.Done()
			if permit := capacity.TryAcquire(); permit != nil {
				accepted <- permit
			}
		}()
	}
	attempts.Wait()
	close(accepted)
	require.Len(t, accepted, 4)
	for permit := range accepted {
		permit.Release()
	}
	assert.True(t, capacity.Snapshot().Ready)
}

func TestCapacityProbeWorksWithoutAWorkPermit(t *testing.T) {
	capacity, err := NewCapacity(Limits{MaxRequests: 1})
	require.NoError(t, err)
	permit := capacity.TryAcquire()
	require.NotNil(t, permit)
	response := httptest.NewRecorder()
	capacity.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/capacityz", nil))
	assert.Equal(t, http.StatusServiceUnavailable, response.Code)
	assert.Contains(t, response.Body.String(), `"memory_usage_bytes":null`)
	assert.Equal(t, 1, capacity.Snapshot().ActiveRequests)
	permit.Release()
	response = httptest.NewRecorder()
	capacity.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/capacityz", nil))
	assert.Equal(t, http.StatusOK, response.Code)
}
