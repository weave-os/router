package translate

import (
	"strings"

	"github.com/tidwall/gjson"
)

const codexRootAgentPath = "/root"

// CodexSubagentPath returns the receiving child from native agent deliveries.
// A full-history fork inherits its parent's deliveries, so the last recipient
// wins. Projected user text and role instructions cannot establish this identity.
func CodexSubagentPath(body []byte) string {
	recipient := codexCurrentAgentPath(gjson.GetBytes(body, "input"))
	if recipient == codexRootAgentPath {
		return ""
	}
	return recipient
}

func codexCurrentAgentPath(input gjson.Result) string {
	recipient := codexRootAgentPath
	input.ForEach(func(_, item gjson.Result) bool {
		if escalationWireType(item.Get("type").String()) != escalationWireAgentMessage {
			return true
		}
		path := item.Get("recipient").String()
		if validCodexAgentPath(path) {
			recipient = path
		}
		return true
	})
	return recipient
}

func validCodexAgentPath(path string) bool {
	if len(path) > 256 || (path != codexRootAgentPath && !strings.HasPrefix(path, codexRootAgentPath+"/")) {
		return false
	}
	for _, segment := range strings.Split(strings.TrimPrefix(path, "/"), "/") {
		if segment == "" {
			return false
		}
		for _, char := range segment {
			if !((char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') || (char >= '0' && char <= '9') || char == '_' || char == '-') {
				return false
			}
		}
	}
	return true
}

// Descendant replies and inherited deliveries are agent work, not new tasks
// for the receiving thread. Parent and peer assignments retain user semantics.
func codexAgentMessageRoutingRole(currentPath string, item gjson.Result) EscalationRole {
	author, recipient := item.Get("author").String(), item.Get("recipient").String()
	if !validCodexAgentPath(author) || !validCodexAgentPath(recipient) {
		return EscalationRoleUser
	}
	if recipient == currentPath && !strings.HasPrefix(author, currentPath+"/") {
		return EscalationRoleUser
	}
	return EscalationRoleAssistant
}
