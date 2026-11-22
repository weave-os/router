package routingpolicy

import (
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/turntype"
)

func AdmitClassifierTurn(turnType turntype.TurnType, inputCaptured bool, request router.Request) error {
	switch turnType {
	case turntype.TitleGen, turntype.Probe, turntype.Compaction, turntype.Classifier:
		return router.ErrClassifierHistoryUnavailable
	case turntype.MainLoop, turntype.ToolResult, turntype.SubAgentDispatch, turntype.Recap:
	}
	if !inputCaptured || request.ForceModel != "" || request.ForceCluster != "" {
		return router.ErrClassifierThreadInvalid
	}
	return nil
}
