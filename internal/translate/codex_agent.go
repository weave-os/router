package translate

import (
	"strings"

	"github.com/tidwall/gjson"
)

// CodexSubagentPath returns the receiving child from native agent deliveries.
// A full-history fork inherits its parent's deliveries, so the last recipient
// wins. Projected user text and role instructions cannot establish this identity.
func CodexSubagentPath(body []byte) string {
	const rootPath = "/root"
	var recipient string
	gjson.GetBytes(body, "input").ForEach(func(_, item gjson.Result) bool {
		if escalationWireType(item.Get("type").String()) != escalationWireAgentMessage {
			return true
		}
		path := item.Get("recipient").String()
		if len(path) > 256 || (path != rootPath && !strings.HasPrefix(path, rootPath+"/")) {
			return true
		}
		for _, segment := range strings.Split(strings.TrimPrefix(path, "/"), "/") {
			if segment == "" {
				return true
			}
			for _, char := range segment {
				if !((char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') || (char >= '0' && char <= '9') || char == '_' || char == '-') {
					return true
				}
			}
		}
		recipient = path
		return true
	})
	if recipient == rootPath {
		return ""
	}
	return recipient
}
