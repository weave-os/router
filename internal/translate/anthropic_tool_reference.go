package translate

import (
	"fmt"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

type anthropicToolNameBlock string

const (
	anthropicToolNameReference anthropicToolNameBlock = "tool_reference"
	anthropicToolNameUse       anthropicToolNameBlock = "tool_use"
	anthropicToolNameResult    anthropicToolNameBlock = "tool_result"
)

func rewriteAnthropicToolReferences(body []byte, aliases map[string]string) ([]byte, error) {
	if len(aliases) == 0 {
		return body, nil
	}
	var edits []anthropicToolNameEdit
	collectAnthropicToolNameEdits(gjson.GetBytes(body, "system"), "system", aliases, false, &edits)
	for i, message := range gjson.GetBytes(body, "messages").Array() {
		collectAnthropicToolNameEdits(message.Get("content"), fmt.Sprintf("messages.%d.content", i), aliases, false, &edits)
	}
	return applyAnthropicToolNameEdits(body, edits)
}

type anthropicToolNameEdit struct {
	path string
	name string
}

func collectAnthropicToolNameEdits(blocks gjson.Result, path string, aliases map[string]string, restoreUses bool, edits *[]anthropicToolNameEdit) {
	if blocks.IsArray() {
		for i, block := range blocks.Array() {
			collectAnthropicToolNameEdits(block, fmt.Sprintf("%s.%d", path, i), aliases, restoreUses, edits)
		}
		return
	}
	switch anthropicToolNameBlock(blocks.Get("type").String()) {
	case anthropicToolNameReference:
		if name, ok := aliases[blocks.Get("name").String()]; ok {
			*edits = append(*edits, anthropicToolNameEdit{path: path + ".name", name: name})
		}
	case anthropicToolNameUse:
		if restoreUses {
			if name, ok := aliases[blocks.Get("name").String()]; ok {
				*edits = append(*edits, anthropicToolNameEdit{path: path + ".name", name: name})
			}
		}
	case anthropicToolNameResult:
		collectAnthropicToolNameEdits(blocks.Get("content"), path+".content", aliases, restoreUses, edits)
	case anthropicToolNameBlock(anthropicSystemOnlyContentToolAddition), anthropicToolNameBlock(anthropicSystemOnlyContentToolRemoval):
		collectAnthropicToolNameEdits(blocks.Get("tool"), path+".tool", aliases, restoreUses, edits)
	}
}

func applyAnthropicToolNameEdits(body []byte, edits []anthropicToolNameEdit) ([]byte, error) {
	var err error
	for _, edit := range edits {
		body, err = sjson.SetBytes(body, edit.path, edit.name)
		if err != nil {
			return nil, fmt.Errorf("rewrite Anthropic tool name: %w", err)
		}
	}
	return body, nil
}
