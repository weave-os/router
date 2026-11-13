package proxy

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"weave-os/router/internal/observability"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/router/taskdomain"
	"weave-os/router/internal/router/turntype"
	"weave-os/router/internal/translate"
)

// TaskDomainResolver keeps optional inference and persistence under one bounded request lifetime.
type TaskDomainResolver struct {
	Store          taskdomain.Store
	Classifier     taskdomain.Classifier
	ReleaseSHA256  string
	EvidenceSHA256 string
}

// Resolve returns only a committed profile; cancelled or late inference cannot influence selection.
func (r *TaskDomainResolver) Resolve(ctx context.Context, input taskdomain.Input) taskdomain.Outcome {
	baseline := taskdomain.Outcome{Status: taskdomain.NoTask, ReleaseSHA256: r.ReleaseSHA256, EvidenceSHA256: r.EvidenceSHA256}
	if r.EvidenceSHA256 == "" {
		baseline.Status = taskdomain.EvidenceUnavailable
		return baseline
	}
	if input.ConversationKey == "" || (!input.Resume && (input.RootSHA256 == "" || input.UserText == "" || len(input.UserText) > taskdomain.MaxInputBytes)) {
		return baseline
	}
	ctx, cancel := context.WithTimeout(ctx, taskdomain.Timeout)
	defer cancel()
	key := taskdomain.Key{Conversation: input.ConversationKey, Root: input.RootSHA256, Release: r.ReleaseSHA256, Evidence: r.EvidenceSHA256}
	outcome, err := r.Store.Resolve(ctx, key, input.Resume, func(inferenceCtx context.Context) taskdomain.Outcome {
		deadline, _ := ctx.Deadline()
		inferenceCtx, stopInference := context.WithDeadline(inferenceCtx, deadline.Add(-100*time.Millisecond))
		defer stopInference()
		profile, classifyErr := r.Classifier.Classify(inferenceCtx, input.UserText)
		if classifyErr == nil {
			classifyErr = profile.Validate()
		}
		computed := baseline
		computed.Status = taskdomain.Ready
		if classifyErr != nil || inferenceCtx.Err() != nil {
			computed.Status = taskdomain.Unavailable
			if errors.Is(classifyErr, context.DeadlineExceeded) || errors.Is(inferenceCtx.Err(), context.DeadlineExceeded) {
				computed.Status = taskdomain.TimedOut
			}
			observability.FromContext(ctx).Warn("Task classification unavailable; retaining baseline", "task_status", computed.Status, "task_release_sha256", r.ReleaseSHA256, "error_type", fmt.Sprintf("%T", classifyErr))
			return computed
		}
		computed.Profile = profile
		return computed
	})
	if err != nil || ctx.Err() != nil {
		baseline.Status = taskdomain.Unavailable
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
			baseline.Status = taskdomain.TimedOut
		}
		observability.FromContext(ctx).Warn("Task profile state unavailable; retaining baseline", "task_status", baseline.Status, "task_release_sha256", r.ReleaseSHA256, "err", err)
		return baseline
	}
	observability.FromContext(ctx).Debug("Task profile resolved", "task_status", outcome.Status, "task_cached", outcome.Cached, "task_release_sha256", r.ReleaseSHA256)
	return outcome
}

func taskDomainInput(ctx context.Context, env *translate.RequestEnvelope, apiKeyID string, turn turntype.TurnType) *taskdomain.Input {
	if apiKeyID == "" || (turn != turntype.MainLoop && turn != turntype.ToolResult && turn != turntype.SubAgentDispatch) {
		return nil
	}
	clientID := clientSessionIDForRequest(ctx, env)
	if clientID == "" {
		return nil
	}
	scope, _ := json.Marshal([]string{clientID, taskdomain.ProjectionVersion})
	// Match the credential-keyed HMAC boundary used by conversation preferences.
	mac := hmac.New(sha256.New, []byte(sessionCredentialIdentity(ctx, apiKeyID)))
	_, _ = mac.Write(scope)
	conversation := requestcontext.ServingStateKey(ctx, mac.Sum(nil))
	input := &taskdomain.Input{ConversationKey: hex.EncodeToString(conversation)}
	var text []string
	commandPrelude := false
	for _, message := range env.ConversationMessagesWithClientText() {
		if message.Role == string(translate.EscalationRoleSystem) || message.Role == string(translate.EscalationRoleDeveloper) {
			continue
		}
		if message.Role != string(translate.EscalationRoleUser) || len(message.ToolResults) > 0 || len(message.ToolCalls) > 0 {
			if len(text) > 0 {
				break
			}
			if !commandPrelude {
				// A truncated history cannot establish a new logical task root.
				input.Resume = true
				return input
			}
			continue
		}
		userText := initialTaskText(message.Text)
		if isTaskResumeText(userText) {
			if len(text) > 0 {
				break
			}
			input.Resume = true
			return input
		}
		if userText == "" {
			if isTaskCommand(strings.TrimSpace(message.Text)) {
				commandPrelude = true
			}
			continue
		}
		if isTaskCommand(userText) {
			commandPrelude = true
			continue
		}
		text = append(text, userText)
	}
	input.UserText = strings.Join(text, "\n\n")
	if input.UserText == "" || len(input.UserText) > taskdomain.MaxInputBytes {
		return nil
	}
	root := sha256.Sum256([]byte(input.UserText))
	input.RootSHA256 = hex.EncodeToString(root[:])
	return input
}

func isTaskResumeText(text string) bool {
	return strings.HasPrefix(text, clientCompactContinuationPrefix) ||
		strings.HasPrefix(text, "Another language model started to solve this problem and produced a summary") ||
		strings.HasPrefix(text, "The conversation history before this point was compacted into the following summary:")
}

type taskControlCommand string

const (
	taskCommandForceAlias   taskControlCommand = "/fm"
	taskCommandForce        taskControlCommand = "/force-model"
	taskCommandUnforceAlias taskControlCommand = "/ufm"
	taskCommandUnforce      taskControlCommand = "/unforce-model"
	taskCommandModel        taskControlCommand = "/model"
	taskCommandClear        taskControlCommand = "/clear"
	taskCommandCompact      taskControlCommand = "/compact"
	taskCommandStatus       taskControlCommand = "/status"
)

func isTaskCommand(text string) bool {
	for strings.HasPrefix(text, "<system-reminder>") {
		_, remaining, complete := strings.Cut(text, "</system-reminder>")
		if !complete {
			return false
		}
		text = strings.TrimSpace(remaining)
	}
	if strings.HasPrefix(text, "<command-") || strings.HasPrefix(text, "<local-command-") {
		return true
	}
	// Do not discard arbitrary slash-prefixed paths or skill requests.
	command, _, _ := strings.Cut(text, " ")
	switch taskControlCommand(command) {
	case taskCommandForceAlias, taskCommandForce, taskCommandUnforceAlias, taskCommandUnforce, taskCommandModel, taskCommandClear, taskCommandCompact, taskCommandStatus:
		return true
	default:
		return false
	}
}

func initialTaskText(text string) string {
	text = strings.TrimSpace(translate.WithoutLeadingClientInjectedText(text))
	if strings.HasPrefix(text, "# AGENTS.md instructions for ") {
		return ""
	}
	for {
		stripped := false
		for _, tag := range []string{"system_instruction", "environment_context"} {
			opening, closing := "<"+tag+">", "</"+tag+">"
			if strings.HasPrefix(text, opening) {
				_, remaining, complete := strings.Cut(text, closing)
				if !complete {
					return ""
				}
				text = strings.TrimSpace(remaining)
				stripped = true
				break
			}
		}
		if !stripped {
			return text
		}
	}
}
