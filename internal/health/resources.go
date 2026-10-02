package health

import (
	"context"
	"io/fs"
	"os"
	"strconv"
	"strings"
	"time"
)

type ResourceSource string

const (
	ResourceUnavailable ResourceSource = "unavailable"
	ResourceCgroupV1    ResourceSource = "cgroup_v1"
	ResourceCgroupV2    ResourceSource = "cgroup_v2"
)

// Nil measurements mean unavailable, never zero usage. CPU is diagnostic only.
// Container memory includes native/model allocations that Go heap stats omit.
type Resources struct {
	Source           ResourceSource `json:"source"`
	SampledAt        time.Time      `json:"sampled_at"`
	MemoryUsageBytes *uint64        `json:"memory_usage_bytes"`
	MemoryLimitBytes *uint64        `json:"memory_limit_bytes"`
	CPUCores         *float64       `json:"cpu_cores"`
	CPUUsageFraction *float64       `json:"cpu_usage_fraction"`
}

// SampleResources establishes the initial local snapshot before startup succeeds.
func (c *Capacity) SampleResources() {
	sampler := containerResourceSampler()
	c.UpdateResources(sampler.sample(time.Now()))
}

// RunResourceSampler reads the container's cgroup mount, outside probe handlers.
// Environments without this accounting expose null measurements; they must be
// verified before enabling a memory gate. No host-memory or Go-heap substitute
// is used for an absent container reading.
func (c *Capacity) RunResourceSampler(ctx context.Context) {
	sampler := containerResourceSampler()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		c.UpdateResources(sampler.sample(time.Now()))
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func containerResourceSampler() resourceSampler {
	// The mounted root is safe only when this process belongs to that root.
	// Host-nested cgroups require mount/ancestor resolution; don't accidentally
	// report a host's memory or CPU allowance as this instance's capacity.
	membership, err := os.ReadFile("/proc/self/cgroup")
	if err != nil || !rootCgroupMembership(string(membership)) {
		return resourceSampler{}
	}
	return resourceSampler{files: os.DirFS("/sys/fs/cgroup")}
}

func rootCgroupMembership(membership string) bool {
	found := false
	for _, line := range strings.Split(strings.TrimSpace(membership), "\n") {
		fields := strings.SplitN(line, ":", 3)
		if len(fields) != 3 {
			return false
		}
		relevant := fields[1] == ""
		for _, controller := range strings.Split(fields[1], ",") {
			if controller == "memory" || controller == "cpu" || controller == "cpuacct" || controller == "cpuset" {
				relevant = true
			}
		}
		if relevant {
			if fields[2] != "/" {
				return false
			}
			found = true
		}
	}
	return found
}

type resourceSampler struct {
	files         fs.FS
	previousTime  time.Time
	previousUsage uint64
	havePrevious  bool
}

func (s *resourceSampler) sample(now time.Time) Resources {
	resources := Resources{Source: ResourceUnavailable, SampledAt: now}
	if s.files == nil {
		return resources
	}
	var cpuUsage *uint64
	if usage, ok := readUint(s.files, "memory.current"); ok {
		resources.Source = ResourceCgroupV2
		resources.MemoryUsageBytes = &usage
		if limit, ok := readUint(s.files, "memory.max"); ok && limit > 0 {
			resources.MemoryLimitBytes = &limit
		}
	} else if usage, ok := readUint(s.files, "memory/memory.usage_in_bytes"); ok {
		resources.Source = ResourceCgroupV1
		resources.MemoryUsageBytes = &usage
		// v1 encodes an unlimited controller using a page-aligned LONG_MAX.
		if limit, ok := readUint(s.files, "memory/memory.limit_in_bytes"); ok && limit > 0 && limit < 1<<60 {
			resources.MemoryLimitBytes = &limit
		}
	}
	if raw, err := fs.ReadFile(s.files, "cpu.max"); err == nil {
		fields := strings.Fields(string(raw))
		if len(fields) == 2 {
			resources.CPUCores = quotaCores(fields[0], fields[1])
		}
		if stat, err := fs.ReadFile(s.files, "cpu.stat"); err == nil {
			fields := strings.Fields(string(stat))
			for i := 0; i+1 < len(fields); i += 2 {
				if fields[i] == "usage_usec" {
					usage, err := strconv.ParseUint(fields[i+1], 10, 64)
					if err == nil {
						usage *= 1000
						cpuUsage = &usage
					}
					break
				}
			}
		}
	} else {
		for _, directory := range []string{"cpu", "cpu,cpuacct"} {
			quota, quotaErr := fs.ReadFile(s.files, directory+"/cpu.cfs_quota_us")
			period, periodErr := fs.ReadFile(s.files, directory+"/cpu.cfs_period_us")
			if quotaErr == nil && periodErr == nil {
				resources.CPUCores = quotaCores(strings.TrimSpace(string(quota)), strings.TrimSpace(string(period)))
				break
			}
		}
		for _, directory := range []string{"cpuacct", "cpu,cpuacct"} {
			if usage, ok := readUint(s.files, directory+"/cpuacct.usage"); ok {
				cpuUsage = &usage
				break
			}
		}
	}
	for _, filename := range []string{"cpuset.cpus.effective", "cpuset/cpuset.effective_cpus", "cpuset/cpuset.cpus"} {
		if raw, err := fs.ReadFile(s.files, filename); err == nil {
			if cores := cpusetCores(string(raw)); cores != nil && resources.CPUCores != nil && *cores < *resources.CPUCores {
				resources.CPUCores = cores
			}
			break
		}
	}
	if cpuUsage != nil && resources.CPUCores != nil {
		if s.havePrevious && now.After(s.previousTime) && *cpuUsage >= s.previousUsage {
			fraction := float64(*cpuUsage-s.previousUsage) / float64(now.Sub(s.previousTime)) / *resources.CPUCores
			resources.CPUUsageFraction = &fraction
		}
		s.previousUsage, s.previousTime, s.havePrevious = *cpuUsage, now, true
	} else {
		s.havePrevious = false
	}
	return resources
}

func readUint(files fs.FS, filename string) (uint64, bool) {
	raw, err := fs.ReadFile(files, filename)
	if err != nil {
		return 0, false
	}
	value, err := strconv.ParseUint(strings.TrimSpace(string(raw)), 10, 64)
	return value, err == nil
}

func quotaCores(quota, period string) *float64 {
	quotaMicros, err := strconv.ParseUint(quota, 10, 64)
	if err != nil || quotaMicros == 0 {
		return nil
	}
	periodMicros, err := strconv.ParseUint(period, 10, 64)
	if err != nil || periodMicros == 0 {
		return nil
	}
	cores := float64(quotaMicros) / float64(periodMicros)
	return &cores
}

func cpusetCores(raw string) *float64 {
	var count uint64
	for _, interval := range strings.Split(strings.TrimSpace(raw), ",") {
		bounds := strings.Split(interval, "-")
		if len(bounds) > 2 {
			return nil
		}
		start, err := strconv.ParseUint(bounds[0], 10, 32)
		if err != nil {
			return nil
		}
		end := start
		if len(bounds) == 2 {
			end, err = strconv.ParseUint(bounds[1], 10, 32)
			if err != nil || end < start {
				return nil
			}
		}
		count += end - start + 1
	}
	if count == 0 {
		return nil
	}
	cores := float64(count)
	return &cores
}
