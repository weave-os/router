package percallband

type Snapshot struct {
	Hist        []Action       `json:"hist"`
	HistWide    []Action       `json:"hist_wide"`
	Cum         map[Action]int `json:"cum"`
	LastPos     map[Action]int `json:"last_pos"`
	NPrior      int            `json:"n_prior"`
	NToolResult int            `json:"n_tool_result"`
	PrevAction  Action         `json:"prev_action"`
	Streak      int            `json:"streak"`
	OutHist     []int          `json:"out_hist"`
	OutSum      float64        `json:"out_sum"`
	FirstTs     int64          `json:"first_ts"`
	PrevTs      int64          `json:"prev_ts"`
	HasPrevTs   bool           `json:"has_prev_ts"`
}

func (s *State) Snapshot() Snapshot {
	return Snapshot{
		Hist:        append([]Action(nil), s.hist...),
		HistWide:    append([]Action(nil), s.histWide...),
		Cum:         copyActionCounts(s.cum),
		LastPos:     copyActionCounts(s.lastPos),
		NPrior:      s.nPrior,
		NToolResult: s.nToolResult,
		PrevAction:  s.prevAction,
		Streak:      s.streak,
		OutHist:     append([]int(nil), s.outHist...),
		OutSum:      s.outSum,
		FirstTs:     s.firstTs,
		PrevTs:      s.prevTs,
		HasPrevTs:   s.hasPrevTs,
	}
}

func StateFromSnapshot(snap Snapshot) *State {
	s := NewState()
	s.hist = append([]Action(nil), snap.Hist...)
	s.histWide = append([]Action(nil), snap.HistWide...)
	if snap.Cum != nil {
		s.cum = copyActionCounts(snap.Cum)
	}
	if snap.LastPos != nil {
		s.lastPos = copyActionCounts(snap.LastPos)
	}
	s.nPrior = snap.NPrior
	s.nToolResult = snap.NToolResult
	s.prevAction = snap.PrevAction
	s.streak = snap.Streak
	s.outHist = append([]int(nil), snap.OutHist...)
	s.outSum = snap.OutSum
	s.firstTs = snap.FirstTs
	s.prevTs = snap.PrevTs
	s.hasPrevTs = snap.HasPrevTs
	return s
}

func copyActionCounts(m map[Action]int) map[Action]int {
	out := make(map[Action]int, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}
