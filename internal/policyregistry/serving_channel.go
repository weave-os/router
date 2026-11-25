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

const BetaChannelModel = "weave-auto-beta"

var ErrServingChannelNotPermitted = errors.New("installation is not permitted on this serving channel")

var ErrServingChannelChanged = errors.New("start a new conversation to change serving channel")

func ParseServingChannel(value string) (ServingChannel, error) {
	switch channel := ServingChannel(value); channel {
	case ChannelStable, ChannelBeta, ChannelInternal:
		return channel, nil
	}
	return "", fmt.Errorf("unknown serving channel %q", value)
}

var targetChannels = map[ServingTarget]ServingChannel{
	TargetStable:   ChannelStable,
	TargetInternal: ChannelInternal,
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
	Worker        ServingTarget
	WorkerChannel ServingChannel
	Enrolled      ServingTarget
	Permissions   []ChannelPermission
}

type ChannelResolution struct {
	Target     ServingTarget
	Generation int64
}

func (a ChannelAdmission) ResolveTarget() (ChannelResolution, error) {
	if a.WorkerChannel == "" || EnrollmentDecidesTarget(a.Worker) {
		return ChannelResolution{Target: a.Enrolled}, nil
	}
	for _, permission := range a.Permissions {
		if permission.Channel == a.WorkerChannel {
			return ChannelResolution{Target: a.Worker, Generation: permission.Generation}, nil
		}
	}
	return ChannelResolution{}, ErrServingChannelNotPermitted
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
