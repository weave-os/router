package translate

import "weave-os/router/internal/router"

func thinkingConfigRaw(effort, model string) string {
	if usesGemini3Contract(model) {
		var level string
		switch effort {
		case "low", "medium", "high":
			level = effort
		case "max", "xhigh":
			level = "high"
			if router.Lookup(model).Supports(router.CapXhighEffort) {
				level = "xhigh"
			}
		default:
			return ""
		}
		fw := newJSONWriter()
		fw.Obj()
		fw.Key("thinkingLevel")
		fw.Str(level)
		fw.EndObj()
		return string(fw.Bytes())
	}

	var budget int64
	switch effort {
	case "none":
		budget = 0
	case "low":
		budget = 1024
	case "medium":
		budget = 8192
	case "high", "max", "xhigh":
		budget = 24576
	default:
		return ""
	}
	fw := newJSONWriter()
	fw.Obj()
	fw.Key("thinkingBudget")
	fw.Int(budget)
	fw.EndObj()
	return string(fw.Bytes())
}
