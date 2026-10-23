package proxy

import "weave-os/router/internal/translate"

// nativeOnlyReasonLimit bounds the reasons logged for one request; a Codex
// tool list can repeat the same unportable shape many times.
const nativeOnlyReasonLimit = 8

// nativeOnlyReasons lists the transforms that kept a Responses request on its
// native wire family, as code@path.
func nativeOnlyReasons(report []translate.ResponseTransform) []string {
	reasons := make([]string, 0, min(len(report), nativeOnlyReasonLimit))
	for _, transform := range report {
		if transform.Action != "preserved" {
			continue
		}
		if len(reasons) == nativeOnlyReasonLimit {
			break
		}
		reasons = append(reasons, transform.Code+"@"+transform.Path)
	}
	return reasons
}
