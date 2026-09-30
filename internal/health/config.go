package health

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

func LimitsFromEnv(defaultMaxRequests int, defaultMaxBufferedBytes int64) (Limits, error) {
	limits, err := limitsFromEnv(os.LookupEnv, defaultMaxRequests, defaultMaxBufferedBytes)
	if err != nil {
		return Limits{}, err
	}
	if limits.MemoryHighBytes > 0 {
		sampler := containerResourceSampler()
		resources := sampler.sample(time.Now())
		if resources.MemoryUsageBytes == nil || resources.MemoryLimitBytes == nil {
			return Limits{}, fmt.Errorf("memory capacity gate requires verified container cgroup usage and a finite limit")
		}
		if limits.MemoryHighBytes >= *resources.MemoryLimitBytes {
			return Limits{}, fmt.Errorf("memory capacity high watermark must be below the container limit")
		}
	}
	return limits, nil
}

func limitsFromEnv(lookup func(string) (string, bool), defaultMaxRequests int, defaultMaxBufferedBytes int64) (Limits, error) {
	parse := func(name string, fallback int64) (int64, error) {
		raw, exists := lookup(name)
		if !exists {
			return fallback, nil
		}
		value, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
		if err != nil || value < 0 {
			return 0, fmt.Errorf("%s must be a nonnegative integer", name)
		}
		return value, nil
	}
	maxRequests, err := parse("ROUTER_CAPACITY_MAX_REQUESTS", int64(defaultMaxRequests))
	if err != nil {
		return Limits{}, err
	}
	resumeRequests, err := parse("ROUTER_CAPACITY_RESUME_REQUESTS", maxRequests/5*4+maxRequests%5*4/5)
	if err != nil {
		return Limits{}, err
	}
	maxBytes, err := parse("ROUTER_CAPACITY_MAX_BUFFERED_BYTES", defaultMaxBufferedBytes)
	if err != nil {
		return Limits{}, err
	}
	resumeBytes, err := parse("ROUTER_CAPACITY_RESUME_BUFFERED_BYTES", maxBytes/4*3+maxBytes%4*3/4)
	if err != nil {
		return Limits{}, err
	}
	memoryHigh, err := parse("ROUTER_CAPACITY_MEMORY_HIGH_BYTES", 0)
	if err != nil {
		return Limits{}, err
	}
	memoryLow, err := parse("ROUTER_CAPACITY_MEMORY_LOW_BYTES", memoryHigh/5*4+memoryHigh%5*4/5)
	if err != nil {
		return Limits{}, err
	}
	limits := Limits{MaxRequests: int(maxRequests), ResumeRequests: int(resumeRequests), MaxBufferedBytes: maxBytes, ResumeBufferedBytes: resumeBytes, MemoryHighBytes: uint64(memoryHigh), MemoryLowBytes: uint64(memoryLow)}
	if int64(limits.MaxRequests) != maxRequests || int64(limits.ResumeRequests) != resumeRequests {
		return Limits{}, fmt.Errorf("request capacity exceeds this platform's integer range")
	}
	if _, err := NewCapacity(limits); err != nil {
		return Limits{}, err
	}
	return limits, nil
}
