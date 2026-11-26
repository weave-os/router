package policyregistry

import (
	"errors"
	"fmt"
)

type ServingChannel string

const (
	ChannelStable   ServingChannel = "stable"
	ChannelBeta     ServingChannel = "beta"
	ChannelInternal ServingChannel = "internal"
)

var ErrServingChannelNotPermitted = errors.New("installation is not permitted on this serving channel")

var ErrServingChannelChanged = errors.New("start a new conversation to change serving channel")

var ErrServingWorkerUnscoped = errors.New("serving admission has no worker identity or channel")

func ParseServingChannel(value string) (ServingChannel, error) {
	if ServingChannel(value) == ChannelBeta {
		return ChannelBeta, nil
	}
	return "", fmt.Errorf("unknown serving channel %q", value)
}

var targetChannels = map[ServingTarget]ServingChannel{
	TargetStable:   ChannelStable,
	TargetInternal: ChannelInternal,
	TargetBeta:     ChannelBeta,
}

func (t ServingTarget) Channel() (ServingChannel, bool) {
	channel, found := targetChannels[t]
	return channel, found
}

type ChannelPermission struct {
	Channel    ServingChannel
	Generation int64
}

type ChannelAdmission struct {
	Worker         ServingTarget
	WorkerChannel  ServingChannel
	EnrolledTarget ServingTarget
	Permissions    []ChannelPermission
}

type ChannelResolution struct {
	Target     ServingTarget
	Generation int64
}

func (a ChannelAdmission) ResolveTarget() (ChannelResolution, error) {
	if a.Worker == "" {
		return ChannelResolution{}, ErrServingWorkerUnscoped
	}
	if EnrollmentDecidesTarget(a.Worker) {
		return ChannelResolution{Target: a.EnrolledTarget}, nil
	}
	if a.WorkerChannel == "" {
		return ChannelResolution{}, ErrServingWorkerUnscoped
	}
	for _, permission := range a.Permissions {
		if permission.Channel == a.WorkerChannel {
			return ChannelResolution{Target: a.Worker, Generation: permission.Generation}, nil
		}
	}
	return ChannelResolution{}, ErrServingChannelNotPermitted
}

func ValidateWorkerChannel(worker ServingTarget, channel ServingChannel) error {
	if worker == "" {
		return ErrServingWorkerUnscoped
	}
	expected, hasChannel := worker.Channel()
	if !hasChannel && EnrollmentDecidesTarget(worker) {
		return nil
	}
	if !hasChannel || channel != expected {
		return fmt.Errorf("%w: worker %q needs channel %q, got %q", ErrServingWorkerUnscoped, worker, expected, channel)
	}
	return nil
}

func EnrollmentDecidesTarget(target ServingTarget) bool {
	switch target {
	case TargetStaging, TargetStable, TargetInternal:
		return true
	default:
		return false
	}
}

func RejectSessionChannelChange(previous *SessionReleaseBinding, target ServingTarget) error {
	if previous == nil || previous.Target == target {
		return nil
	}
	if EnrollmentDecidesTarget(previous.Target) && EnrollmentDecidesTarget(target) {
		return nil
	}
	return ErrServingChannelChanged
}
