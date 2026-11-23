package proxyhmmhistory

import "weave-os/router/internal/router/sessionpin"

type HMMTurnRecord struct{ Role string }

func LoadHMMHistory(pins map[string]sessionpin.Pin, role string) sessionpin.Pin {
	return pins[role+"_hmm_history"]
}

func serve(pins map[string]sessionpin.Pin) HMMTurnRecord {
	hmmHistory := LoadHMMHistory(pins, sessionpin.DefaultRole)
	return HMMTurnRecord{Role: hmmHistory.Role}
}
