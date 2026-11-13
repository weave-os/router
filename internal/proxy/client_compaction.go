package proxy

import (
	"context"
	"fmt"
	"strings"

	"weave-os/router/internal/observability"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/turntype"
	"weave-os/router/internal/translate"
)

const clientRecoveryAssistantTurns = 3

const clientCompactContinuationPrefix = "This session is being continued from a previous conversation that ran out of context."

const clientCompactionGuidance = `Write a compact working-state handoff, aiming for at most %d tokens of summary text. Preserve the user's goal, constraints, decisions, changed files, verified results, unresolved errors and the exact next action. Reference paths and line ranges instead of copying source code, logs, instructions or previous summaries. Do not omit unfinished work to meet the target.
In the continuation instructions, explain that the client restores instructions and the latest complete tool batch separately. Resume from the recorded next action rather than rereading every file. Retrieve only missing evidence, using bounded line ranges or filtered output, one small tool result at a time until useful work resumes. A larger provider window does not enlarge the client's compaction budget.`

func applyClientCompactionRecovery(ctx context.Context, env *translate.RequestEnvelope, budget router.ClientBudget, turn turntype.TurnType, history []router.ConversationMessage) (bool, error) {
	if budget.Evidence != router.ClientBudgetHarnessDefault || env.SourceFormat() != translate.FormatAnthropic {
		return false, nil
	}
	log := observability.FromContext(ctx)
	if turn == turntype.Compaction {
		// A textual target leaves room for reasoning and does not cut off a valid handoff.
		summaryTargetTokens := min(DefaultCompactionMaxTokens, budget.DefaultWindow/100)
		err := env.AppendClientCompactionInstruction(fmt.Sprintf(clientCompactionGuidance, summaryTargetTokens))
		if err != nil {
			log.Error("Failed to add client compaction guidance", "err", err)
			return false, err
		}
		log.Debug("Applied client compaction guidance", "summary_target_tokens", summaryTargetTokens, "budget_evidence", budget.Evidence)
		return true, nil
	}
	continuation := env.FirstUserMessageText()
	for _, message := range history {
		if message.Role == "user" && strings.HasPrefix(message.Text, clientCompactContinuationPrefix) {
			continuation = message.Text
			break
		}
	}
	if (turn != turntype.MainLoop && turn != turntype.ToolResult) || budget.DefaultWindow > claudeCodeDefaultWindow ||
		!env.HasTools() || !strings.HasPrefix(continuation, clientCompactContinuationPrefix) {
		return false, nil
	}
	// Bound the recovery period from this request's history, not a session cache.
	// Include the preserved assistant batch; ordinary parallel work resumes after it.
	assistantTurns := 0
	for _, message := range history {
		if message.Role == "assistant" {
			assistantTurns++
		}
	}
	if assistantTurns > clientRecoveryAssistantTurns {
		return false, nil
	}
	// Only a request parameter changes: editing the resumed transcript would
	// invalidate signed thinking and would not change the client's retained batch.
	changed, err := env.DisableParallelToolUse()
	if err != nil {
		log.Error("Failed to constrain compaction recovery tool batch", "err", err)
		return false, err
	}
	if changed {
		log.Debug("Constrained compaction recovery tool batch", "client_default_window", budget.DefaultWindow)
	}
	return true, nil
}
