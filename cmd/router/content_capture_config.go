package main

import (
	"weave-os/router/internal/config"
	"weave-os/router/internal/server"
	"weave-os/router/internal/telemetry/capture"
)

func contentCaptureModeFromEnv() capture.Mode {
	return capture.ParseMode(config.GetOr("WV_CAPTURE_CONTENT", ""))
}

func withContentCaptureMode(features server.Features, mode capture.Mode) server.Features {
	features.ContentCaptureMode = mode
	return features
}
