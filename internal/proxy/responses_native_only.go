package proxy

import (
	"github.com/tidwall/gjson"

	"weave-os/router/internal/translate"
)

// nativeOnlyReasonLimit bounds the reasons logged for one request; a Codex
// tool list can repeat the same unportable shape many times.
const nativeOnlyReasonLimit = 8

// nativeOnlyReasons lists the transforms that kept a Responses request on its
// native wire family, as code@path, plus :type when the item at path declares one.
func nativeOnlyReasons(report []translate.ResponseTransform, body []byte) []string {
	reasons := make([]string, 0, min(len(report), nativeOnlyReasonLimit))
	for _, transform := range report {
		if transform.Action != "preserved" {
			continue
		}
		if len(reasons) == nativeOnlyReasonLimit {
			break
		}
		reason := transform.Code + "@" + transform.Path
		if itemType := gjson.GetBytes(body, transform.Path+".type").String(); itemType != "" {
			reason += ":" + itemType
		}
		reasons = append(reasons, reason)
	}
	return reasons
}
