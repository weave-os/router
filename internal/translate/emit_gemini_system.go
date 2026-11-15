package translate

import "github.com/tidwall/gjson"

func openAISystemTextParts(msgs gjson.Result) []string {
	var sysParts []string
	msgs.ForEach(func(_, msg gjson.Result) bool {
		if msg.Get("role").String() != "system" {
			return true
		}
		content := msg.Get("content")
		switch content.Type {
		case gjson.String:
			if s := content.String(); s != "" {
				sysParts = append(sysParts, s)
			}
		case gjson.JSON:
			content.ForEach(func(_, part gjson.Result) bool {
				if part.Get("type").String() == "text" {
					if s := part.Get("text").String(); s != "" {
						sysParts = append(sysParts, s)
					}
				}
				return true
			})
		default:
		}
		return true
	})
	return sysParts
}
