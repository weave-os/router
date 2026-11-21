package policyclient

import (
	"encoding/json"
	"fmt"
	"strings"
)

type classifierValidationIssue struct {
	Location []any  `json:"loc"`
	Message  string `json:"msg"`
}

func classifierErrorDetail(raw json.RawMessage) string {
	var message string
	if err := json.Unmarshal(raw, &message); err == nil {
		return message
	}
	var issues []classifierValidationIssue
	if err := json.Unmarshal(raw, &issues); err != nil {
		return ""
	}
	rendered := make([]string, 0, len(issues))
	for _, issue := range issues {
		location := make([]string, 0, len(issue.Location))
		for _, segment := range issue.Location {
			location = append(location, fmt.Sprint(segment))
		}
		rendered = append(rendered, strings.Join(location, ".")+": "+issue.Message)
	}
	return strings.Join(rendered, "; ")
}
