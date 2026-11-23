package iolog

import (
	"log/slog"
	"strings"

	"weave-os/router/internal/translate"
)

func logInboundToolTraffic(log *slog.Logger, env *translate.RequestEnvelope) {
	if env == nil {
		return
	}
	const tailWindow = 5
	sigs := env.AssistantToolCallSignatures()
	if len(sigs) == 0 {
		return
	}
	offset := len(sigs) - tailWindow
	if offset < 0 {
		offset = 0
	}
	args := env.AssistantToolCallArgsPreview(offset, 160)
	names := make([]string, 0, len(sigs))
	for _, s := range sigs {
		names = append(names, s.Name)
	}
	log.Debug("inbound tool_use history",
		"total_tool_calls", len(sigs),
		"tool_names", names,
		"tail_window_args", args,
	)
}

func LogInboundRequestDiagnostics(log *slog.Logger, env *translate.RequestEnvelope) {
	logInboundToolTraffic(log, env)
	logInboundConversationTail(log, env)
	logInboundSystemTail(log, env)
}

func logInboundConversationTail(log *slog.Logger, env *translate.RequestEnvelope) {
	if env == nil {
		return
	}
	const tailWindow = 4
	const maxLen = 500
	msgs := env.MessageTailPreview(tailWindow, maxLen)
	if len(msgs) == 0 {
		return
	}
	lines := make([]string, 0, len(msgs))
	for _, m := range msgs {
		var b strings.Builder
		b.WriteString(m.Role)
		b.WriteString(" :: ")
		for i, blk := range m.Blocks {
			if i > 0 {
				b.WriteString(" || ")
			}
			b.WriteString(blk.Type)
			if blk.Name != "" {
				b.WriteByte('[')
				b.WriteString(blk.Name)
				b.WriteByte(']')
			}
			if blk.Preview != "" {
				b.WriteString(": ")
				b.WriteString(strings.ReplaceAll(blk.Preview, "\n", " "))
			}
		}
		lines = append(lines, b.String())
	}
	log.Info("inbound conversation tail",
		"window", tailWindow,
		"max_len", maxLen,
		"messages", lines,
	)
}

func logInboundSystemTail(log *slog.Logger, env *translate.RequestEnvelope) {
	if env == nil {
		return
	}
	const maxLen = 500
	length, head, tail := env.SystemTextTail(maxLen)
	if length == 0 {
		return
	}
	log.Info("inbound system tail",
		"system_len", length,
		"system_head", head,
		"system_tail", tail,
	)
}
