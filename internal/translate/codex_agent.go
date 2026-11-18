package translate

import (
	"strings"

	"github.com/tidwall/gjson"
)

const codexRootAgentPath = "/root"

func codexSubagentPathFromView(body []byte) string {
	recipient := codexCurrentAgentPath(viewJSONPath(body, "input"))
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
			if !isIdentifierChar(char) {
				return false
			}
		}
	}
	return true
}

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

func isIdentifierChar(c rune) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_' || c == '-'
}
