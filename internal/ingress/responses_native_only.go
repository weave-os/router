package ingress

import (
	"github.com/tidwall/gjson"

	"weave-os/router/internal/translate"
)

const NativeOnlyReasonLimit = 8

func NativeOnlyReasons(report []translate.ResponseTransform, body []byte) []string {
	reasons := make([]string, 0, min(len(report), NativeOnlyReasonLimit))
	for _, transform := range report {
		if transform.Action != "preserved" {
			continue
		}
		if len(reasons) == NativeOnlyReasonLimit {
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
