package health

import (
	"testing"
	"testing/fstest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCgroupV2IncludesNativeMemoryAndUsesEffectiveCPUQuota(t *testing.T) {
	files := fstest.MapFS{
		"memory.current":        {Data: []byte("734003200\n")},
		"memory.max":            {Data: []byte("1073741824\n")},
		"cpu.max":               {Data: []byte("200000 100000\n")},
		"cpu.stat":              {Data: []byte("usage_usec 100000\nuser_usec 50000\n")},
		"cpuset.cpus.effective": {Data: []byte("0-7\n")},
	}
	sampler := resourceSampler{files: files}
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	first := sampler.sample(now)
	require.NotNil(t, first.MemoryUsageBytes)
	assert.EqualValues(t, 734003200, *first.MemoryUsageBytes)
	assert.EqualValues(t, 1073741824, *first.MemoryLimitBytes)
	assert.Equal(t, 2.0, *first.CPUCores)
	assert.Nil(t, first.CPUUsageFraction, "one sample cannot establish utilization")
	files["cpu.stat"].Data = []byte("usage_usec 1100000\n")
	second := sampler.sample(now.Add(time.Second))
	require.NotNil(t, second.CPUUsageFraction)
	assert.Equal(t, 0.5, *second.CPUUsageFraction)
	files["cpuset.cpus.effective"].Data = []byte("3\n")
	third := sampler.sample(now.Add(2 * time.Second))
	assert.Equal(t, 1.0, *third.CPUCores)
}

func TestCgroupV1UnlimitedAndMissingReadingsRemainUnknown(t *testing.T) {
	files := fstest.MapFS{
		"memory/memory.usage_in_bytes":  {Data: []byte("4096\n")},
		"memory/memory.limit_in_bytes":  {Data: []byte("9223372036854771712\n")},
		"cpu,cpuacct/cpu.cfs_quota_us":  {Data: []byte("-1\n")},
		"cpu,cpuacct/cpu.cfs_period_us": {Data: []byte("100000\n")},
		"cpu,cpuacct/cpuacct.usage":     {Data: []byte("1000000000\n")},
		"cpuset/cpuset.cpus":            {Data: []byte("0-63\n")},
	}
	sampler := resourceSampler{files: files}
	sample := sampler.sample(time.Now())
	assert.Equal(t, ResourceCgroupV1, sample.Source)
	require.NotNil(t, sample.MemoryUsageBytes)
	assert.EqualValues(t, 4096, *sample.MemoryUsageBytes)
	assert.Nil(t, sample.MemoryLimitBytes)
	assert.Nil(t, sample.CPUCores)
	assert.Nil(t, sample.CPUUsageFraction)
	sampler.files = fstest.MapFS{}
	sample = sampler.sample(time.Now())
	assert.Equal(t, ResourceUnavailable, sample.Source)
	assert.Nil(t, sample.MemoryUsageBytes)
	assert.Nil(t, sample.MemoryLimitBytes)
	assert.Nil(t, sample.CPUUsageFraction)
}

func TestResourceSamplerDoesNotReportCounterResetAsCPUUsage(t *testing.T) {
	files := fstest.MapFS{
		"cpu.max":  {Data: []byte("100000 100000")},
		"cpu.stat": {Data: []byte("usage_usec 900000")},
	}
	sampler := resourceSampler{files: files}
	now := time.Now()
	sampler.sample(now)
	files["cpu.stat"].Data = []byte("usage_usec 1000")
	assert.Nil(t, sampler.sample(now.Add(time.Second)).CPUUsageFraction)
}

func TestNonRootCgroupMembershipCannotUseHostRootAccounting(t *testing.T) {
	for _, test := range []struct {
		membership string
		want       bool
	}{
		{"0::/\n", true},
		{"0::/system.slice/router.service\n", false},
		{"5:cpu,cpuacct:/\n4:memory:/\n", true},
		{"5:cpu,cpuacct:/\n4:memory:/docker/another-scope\n", false},
		{"", false},
	} {
		assert.Equal(t, test.want, rootCgroupMembership(test.membership), test.membership)
	}
}
