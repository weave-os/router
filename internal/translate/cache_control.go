package translate

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/tidwall/gjson"
)

var (
	// ErrAnthropicCacheControlInvalid marks an explicit cache policy Anthropic
	// would reject. It is never silently stripped or rewritten.
	ErrAnthropicCacheControlInvalid = errors.New("invalid Anthropic cache_control")
	// ErrAnthropicCacheControlOverflow marks explicit client breakpoints beyond
	// the provider's supported capacity.
	ErrAnthropicCacheControlOverflow = errors.New("Anthropic cache_control breakpoint capacity exceeded")
)

const anthropicCacheControlCapacity = 4

// applyAnthropicCachePolicy validates client controls and uses only spare
// provider capacity for router-generated cache breakpoints.
func applyAnthropicCachePolicy(body []byte, injectRouterBreakpoints bool) ([]byte, error) {
	var request map[string]any
	if err := json.Unmarshal(body, &request); err != nil {
		return nil, fmt.Errorf("decode Anthropic cache policy: %w", err)
	}
	count, err := validateAnthropicCacheControls(request)
	if err != nil {
		return nil, err
	}
	if count > anthropicCacheControlCapacity {
		return nil, fmt.Errorf("%w: got %d, maximum is %d", ErrAnthropicCacheControlOverflow, count, anthropicCacheControlCapacity)
	}
	if !injectRouterBreakpoints {
		return body, nil
	}
	remaining := anthropicCacheControlCapacity - count
	if remaining == 0 {
		return body, nil
	}
	if undo, ok := addCacheControlToLastSystemBlock(request); ok {
		if _, err := validateAnthropicCacheControls(request); err != nil {
			undo()
		} else {
			remaining--
		}
	}
	if remaining > 0 {
		if undo, ok := addCacheControlToLastMessageBlock(request); ok {
			if _, err := validateAnthropicCacheControls(request); err != nil {
				undo()
			}
		}
	}
	count, err = validateAnthropicCacheControls(request)
	if err != nil {
		return nil, err
	}
	if count > anthropicCacheControlCapacity {
		return nil, fmt.Errorf("%w: got %d, maximum is %d", ErrAnthropicCacheControlOverflow, count, anthropicCacheControlCapacity)
	}
	encoded, err := json.Marshal(request)
	if err != nil {
		return nil, fmt.Errorf("encode Anthropic cache policy: %w", err)
	}
	return encoded, nil
}

type anthropicCacheControlTTL int

const (
	anthropicCacheControlTTL1h anthropicCacheControlTTL = iota
	anthropicCacheControlTTL5m
)

const (
	anthropicCacheTTLWire5m = "5m"
	anthropicCacheTTLWire1h = "1h"
)

// forEachAnthropicCacheBlock visits every tool, system and message content
// block that carries a cache_control, in prompt order.
func forEachAnthropicCacheBlock(request map[string]any, visit func(block map[string]any) error) error {
	visitBlocks := func(blocks any) error {
		list, _ := blocks.([]any)
		for _, block := range list {
			object, ok := block.(map[string]any)
			if !ok {
				continue
			}
			if _, exists := object["cache_control"]; !exists {
				continue
			}
			if err := visit(object); err != nil {
				return err
			}
		}
		return nil
	}
	if err := visitBlocks(request["tools"]); err != nil {
		return err
	}
	if err := visitBlocks(request["system"]); err != nil {
		return err
	}
	messages, _ := request["messages"].([]any)
	for _, message := range messages {
		object, _ := message.(map[string]any)
		if err := visitBlocks(object["content"]); err != nil {
			return err
		}
	}
	return nil
}

func validateAnthropicCacheControls(request map[string]any) (int, error) {
	count := 0
	seenFiveMinuteTTL := false
	err := forEachAnthropicCacheBlock(request, func(block map[string]any) error {
		ttl, err := validateAnthropicCacheControl(block["cache_control"])
		if err != nil {
			return err
		}
		if seenFiveMinuteTTL && ttl == anthropicCacheControlTTL1h {
			return fmt.Errorf("%w: ttl=1h cache_control must not follow ttl=5m", ErrAnthropicCacheControlInvalid)
		}
		if ttl == anthropicCacheControlTTL5m {
			seenFiveMinuteTTL = true
		}
		count++
		return nil
	})
	if err != nil {
		return 0, err
	}
	return count, nil
}

// firstPartyCacheControlBetas maps the cache_control fields only first-party
// Anthropic accepts to the anthropic-beta prefix that enables each one.
var firstPartyCacheControlBetas = map[string]string{
	"scope":             "prompt-caching-scope-",
	"evict_on_complete": "prompt-caching-evict-",
}

// dropUnsupportedCacheControlFields removes first-party cache_control fields
// the target would reject: all of them on gateways, and on Anthropic any field
// whose beta the client did not send.
func dropUnsupportedCacheControlFields(body []byte, firstParty bool, beta string) ([]byte, error) {
	if !bytes.Contains(body, []byte(`"scope"`)) && !bytes.Contains(body, []byte(`"evict_on_complete"`)) {
		return body, nil
	}
	var request map[string]any
	if err := json.Unmarshal(body, &request); err != nil {
		return nil, fmt.Errorf("decode Anthropic cache policy: %w", err)
	}
	dropped := false
	_ = forEachAnthropicCacheBlock(request, func(block map[string]any) error {
		policy, _ := block["cache_control"].(map[string]any)
		for field, prefix := range firstPartyCacheControlBetas {
			if _, ok := policy[field]; !ok {
				continue
			}
			if firstParty && joinKept(beta, func(token string) bool { return strings.HasPrefix(token, prefix) }) != "" {
				continue
			}
			delete(policy, field)
			dropped = true
		}
		return nil
	})
	if !dropped {
		return body, nil
	}
	encoded, err := json.Marshal(request)
	if err != nil {
		return nil, fmt.Errorf("encode Anthropic cache policy: %w", err)
	}
	return encoded, nil
}

func validateAnthropicCacheControl(v any) (anthropicCacheControlTTL, error) {
	policy, ok := v.(map[string]any)
	if !ok || policy["type"] != "ephemeral" {
		return anthropicCacheControlTTL5m, fmt.Errorf("%w: expected type=ephemeral", ErrAnthropicCacheControlInvalid)
	}
	ttl := anthropicCacheControlTTL5m
	for key, value := range policy {
		switch key {
		case "type":
		case "ttl":
			value, ok := value.(string)
			if !ok || (value != anthropicCacheTTLWire5m && value != anthropicCacheTTLWire1h) {
				return anthropicCacheControlTTL5m, fmt.Errorf("%w: ttl must be 5m or 1h", ErrAnthropicCacheControlInvalid)
			}
			if value == anthropicCacheTTLWire1h {
				ttl = anthropicCacheControlTTL1h
			}
		case "scope":
			if value, ok := value.(string); !ok || value == "" {
				return anthropicCacheControlTTL5m, fmt.Errorf("%w: scope must be a string", ErrAnthropicCacheControlInvalid)
			}
		case "evict_on_complete":
			if _, ok := value.(bool); !ok {
				return anthropicCacheControlTTL5m, fmt.Errorf("%w: evict_on_complete must be a boolean", ErrAnthropicCacheControlInvalid)
			}
		default:
			return anthropicCacheControlTTL5m, fmt.Errorf("%w: unsupported field %q", ErrAnthropicCacheControlInvalid, key)
		}
	}
	return ttl, nil
}

func addCacheControlToLastSystemBlock(request map[string]any) (func(), bool) {
	system, ok := request["system"].([]any)
	if !ok || len(system) == 0 {
		return nil, false
	}
	last, ok := system[len(system)-1].(map[string]any)
	if !ok {
		return nil, false
	}
	if _, exists := last["cache_control"]; exists {
		return nil, false
	}
	last["cache_control"] = map[string]any{"type": "ephemeral"}
	return func() { delete(last, "cache_control") }, true
}

func addCacheControlToLastMessageBlock(request map[string]any) (func(), bool) {
	messages, ok := request["messages"].([]any)
	if !ok || len(messages) == 0 {
		return nil, false
	}
	message, ok := messages[len(messages)-1].(map[string]any)
	if !ok {
		return nil, false
	}
	blocks, ok := message["content"].([]any)
	promoted := false
	originalContent := message["content"]
	if !ok {
		if content, ok := message["content"].(string); ok {
			blocks = []any{map[string]any{"type": "text", "text": content}}
			message["content"] = blocks
			promoted = true
		} else {
			return nil, false
		}
	}
	if len(blocks) == 0 {
		return nil, false
	}
	last, ok := blocks[len(blocks)-1].(map[string]any)
	if !ok {
		return nil, false
	}
	if _, exists := last["cache_control"]; exists {
		return nil, false
	}
	last["cache_control"] = map[string]any{"type": "ephemeral"}
	return func() {
		delete(last, "cache_control")
		if promoted {
			message["content"] = originalContent
		}
	}, true
}

// AnthropicRequestCacheTTL1h reports whether an Anthropic Messages body
// declares at least one cache breakpoint and every breakpoint — block-level
// and the top-level automatic one — asks for the 1-hour TTL. Callers use it
// to price cache writes when the upstream omits usage.cache_creation; a body
// that mixes TTLs reports false because its writes cannot be attributed.
func AnthropicRequestCacheTTL1h(body []byte) bool {
	declared, all1h := 0, true
	observe := func(policy gjson.Result) {
		if !policy.Exists() {
			return
		}
		declared++
		if policy.Get("ttl").String() != anthropicCacheTTLWire1h {
			all1h = false
		}
	}
	observeBlocks := func(blocks gjson.Result) {
		blocks.ForEach(func(_, block gjson.Result) bool {
			observe(block.Get("cache_control"))
			return true
		})
	}
	request := gjson.ParseBytes(body)
	observe(request.Get("cache_control"))
	observeBlocks(request.Get("tools"))
	observeBlocks(request.Get("system"))
	request.Get("messages").ForEach(func(_, message gjson.Result) bool {
		observeBlocks(message.Get("content"))
		return true
	})
	return declared > 0 && all1h
}
