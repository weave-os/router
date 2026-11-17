package translate

import (
	"strings"

	"github.com/tidwall/gjson"
)

func (t *AnthropicSSETranslator) emitToolCallDelta(tc gjson.Result) error {
	idx := int(tc.Get("index").Int())
	entry := t.toolLedger.AppendArguments(idx, tc.Get("id").String(), tc.Get("function.name").String(), tc.Get("function.arguments").String())
	if entry.Name == "" {
		return nil
	}
	if err := t.lifecycle.Output(t.blockIdx); err != nil {
		return err
	}
	if _, ok := t.toolBlocks[idx]; ok {
		return nil
	}
	sig := tc.Get("function.thought_signature").Str
	if sig == "" {
		sig = tc.Get("thought_signature").Str
	}
	if len(t.toolBlocks) > 0 {
		if t.toolSignatures[idx] == "" {
			t.toolSignatures[idx] = strings.Clone(sig)
		}
		return nil
	}
	return t.openToolUseBlock(idx, entry.Name, sig)
}

func (t *AnthropicSSETranslator) openToolUseBlock(idx int, name, sig string) error {
	id := t.toolLedger.ProjectID(idx, t.uniqueToolUseID)
	if t.textOpen {
		if err := t.emitContentBlockStop(t.blockIdx - 1); err != nil {
			return err
		}
		t.textOpen = false
	}
	if t.thinkingOpen {
		if err := t.emitContentBlockStop(t.blockIdx - 1); err != nil {
			return err
		}
		t.thinkingOpen = false
	}
	blockIdx := t.blockIdx
	t.toolBlocks[idx] = blockIdx
	t.toolUseEmitted = true
	t.toolUseCount++
	t.blockIdx++
	return t.emitContentBlockStartTool(blockIdx, id, name, sig)
}

func (t *AnthropicSSETranslator) finishToolBlocks() error {
	entries := t.toolLedger.Entries()
	for _, entry := range entries {
		if blockIdx, opened := t.toolBlocks[entry.SourceIndex]; opened {
			if err := t.closeToolUseBlock(entry.SourceIndex, blockIdx); err != nil {
				return err
			}
		}
	}
	for _, entry := range entries {
		if _, opened := t.toolBlocks[entry.SourceIndex]; opened {
			continue
		}
		if entry.Name == "" {
			entry.Accept(t.toolValidator)
			continue
		}
		if err := t.openToolUseBlock(entry.SourceIndex, entry.Name, t.toolSignatures[entry.SourceIndex]); err != nil {
			return err
		}
		if err := t.closeToolUseBlock(entry.SourceIndex, t.toolBlocks[entry.SourceIndex]); err != nil {
			return err
		}
	}
	t.toolBlocks = map[int]int{}
	t.toolSignatures = map[int]string{}
	return nil
}

func (t *AnthropicSSETranslator) closeToolUseBlock(sourceIndex, blockIdx int) error {
	if err := t.emitValidatedToolArgsDelta(sourceIndex, blockIdx); err != nil {
		return err
	}
	return t.emitContentBlockStop(blockIdx)
}

func (t *AnthropicSSETranslator) emitValidatedToolArgsDelta(sourceIndex, blockIdx int) error {
	entry, _ := t.toolLedger.Entry(sourceIndex)
	call := entry.Accept(t.toolValidator)
	if call.Issue != nil {
		t.toolCallIssues = append(t.toolCallIssues, *call.Issue)
	}
	return t.emitContentBlockDeltaJSON(blockIdx, call.Arguments)
}
