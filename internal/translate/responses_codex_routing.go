package translate

import (
	"encoding/json"
	"fmt"
	"maps"
)

func (c *portableCodexResponsesConverter) routingBody(out map[string]any, messages []map[string]any, systemCount int) ([]byte, error) {
	if len(c.routingRoles) == 0 && len(c.toolImageRoutingCallIDs) == 0 && len(c.routingOmitted) == 0 && len(c.routingOriginals) == 0 {
		return nil, nil
	}
	routingMessages := append([]map[string]any(nil), messages...)
	for index, original := range c.routingOriginals {
		routingMessages[index+systemCount] = original
	}
	for index, role := range c.routingRoles {
		index += systemCount
		routingMessages[index] = maps.Clone(routingMessages[index])
		routingMessages[index]["role"] = role
	}
	for index, callID := range c.toolImageRoutingCallIDs {
		index += systemCount
		routingMessages[index] = maps.Clone(routingMessages[index])
		routingMessages[index]["role"] = EscalationRoleTool
		routingMessages[index]["tool_call_id"] = callID
	}
	kept := routingMessages[:0]
	for index, message := range routingMessages {
		if !c.routingOmitted[index-systemCount] {
			kept = append(kept, message)
		}
	}
	out["messages"] = kept
	body, err := json.Marshal(out)
	if err != nil {
		return nil, fmt.Errorf("marshal Codex routing history: %w", err)
	}
	return body, nil
}
