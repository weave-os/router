package percallband

import (
	"fmt"
	"math"
)

const (
	lastK       = 5
	lastKWide   = 12
	sinceCap    = 50
	longGapSecs = 300
)

var lags = [...]int{1, 2, 3, 4, 5}

var extBuckets = [...]string{"code", "config", "docs", "data", "other", "none"}

var (
	extCode   = set("go", "ts", "tsx", "js", "jsx", "py", "rs", "java", "kt", "c", "cc", "cpp", "h", "hpp", "rb", "php", "cs", "swift", "scala", "sql", "vue", "svelte")
	extConfig = set("json", "yaml", "yml", "toml", "ini", "env", "cfg", "conf", "lock", "xml")
	extDocs   = set("md", "mdx", "txt", "rst", "adoc")
	extData   = set("csv", "tsv", "parquet", "db", "sqlite")
)

const FeatureCount = 80

type Call struct {
	TurnType             string
	StepIdx              int
	Ts                   int64
	EstimatedInputTokens int
	LastIsError          bool
	LastFileExt          string
}

type State struct {
	hist        []Action
	histWide    []Action
	cum         map[Action]int
	lastPos     map[Action]int
	nPrior      int
	nToolResult int
	prevAction  Action
	streak      int
	outHist     []int
	outSum      float64
	firstTs     int64
	prevTs      int64
	hasPrevTs   bool
}

func NewState() *State {
	return &State{cum: map[Action]int{}, lastPos: map[Action]int{}}
}

func (s *State) Advance(action Action, ts int64, outputTokens int, isToolResult bool) {
	if s.cum == nil {
		s.cum = map[Action]int{}
	}
	if s.lastPos == nil {
		s.lastPos = map[Action]int{}
	}
	if s.nPrior == 0 {
		s.firstTs = ts
	}
	if s.nPrior > 0 && action == s.prevAction {
		s.streak++
	} else {
		s.streak = 1
	}
	s.prevAction = action
	s.hist = appendRing(s.hist, action, lastK)
	s.histWide = appendRing(s.histWide, action, lastKWide)
	s.cum[action]++
	s.nPrior++
	s.lastPos[action] = s.nPrior
	if isToolResult {
		s.nToolResult++
	}
	s.outHist = appendIntRing(s.outHist, outputTokens, 2)
	s.outSum += float64(outputTokens)
	s.prevTs = ts
	s.hasPrevTs = true
}

func (s *State) Features(c Call) []float32 {
	out := make([]float32, 0, FeatureCount)
	put := func(v float64) { out = append(out, float32(v)) }

	for _, lag := range lags {
		idx := noneIndex
		if len(s.hist) >= lag {
			idx = actionIndex[s.hist[len(s.hist)-lag]]
		}
		for k := 0; k <= noneIndex; k++ {
			put(b2f(k == idx))
		}
	}
	for _, a := range ActionTypes {
		put(float64(countAction(s.hist, a)))
	}
	for _, a := range ActionTypes {
		put(float64(countAction(s.histWide, a)))
	}
	for _, a := range ActionTypes {
		put(cumFrac(s.cum[a], s.nPrior))
	}
	for _, a := range ActionTypes {
		dist := sinceCap
		if p, ok := s.lastPos[a]; ok {
			dist = s.nPrior + 1 - p
		}
		put(log1p(float64(minInt(dist, sinceCap))))
	}
	put(float64(s.streak))
	if s.nPrior == 0 {
		put(0.0)
	} else {
		put(float64(len(ActionTypes)))
	}
	put(s.entropy())
	put(cumFrac(s.nToolResult, s.nPrior))
	isTR := c.TurnType == "tool_result"
	put(b2f(isTR))
	put(b2f(c.TurnType == "main_loop"))
	put(float64(c.StepIdx))
	if s.hasPrevTs {
		gap := float64(c.Ts - s.prevTs)
		put(log1p(gap))
	} else {
		put(-1.0)
	}
	firstTs := s.firstTs
	if s.nPrior == 0 {
		firstTs = c.Ts
	}
	put(log1p(float64(c.Ts - firstTs)))
	longGap := s.hasPrevTs && float64(c.Ts-s.prevTs) > longGapSecs
	put(b2f(longGap))
	put(log1p(float64(c.EstimatedInputTokens)))
	put(prevOut(s.outHist, 1))
	put(prevOut(s.outHist, 2))
	if s.nPrior > 0 {
		put(log1p(s.outSum / float64(s.nPrior)))
	} else {
		put(0.0)
	}
	put(b2f(c.LastIsError))
	bucket := extBucket(c.LastFileExt)
	for _, b := range extBuckets {
		put(b2f(b == bucket))
	}
	return out
}

func (s *State) entropy() float64 {
	if s.nPrior == 0 {
		return 0.0
	}
	var e float64
	for _, a := range ActionTypes {
		n := s.cum[a]
		if n == 0 {
			continue
		}
		p := float64(n) / float64(s.nPrior)
		e -= p * math.Log(p)
	}
	return e
}

func extBucket(ext string) string {
	if ext == "" {
		return "none"
	}
	e := lower(ext)
	switch {
	case has(extCode, e):
		return "code"
	case has(extConfig, e):
		return "config"
	case has(extDocs, e):
		return "docs"
	case has(extData, e):
		return "data"
	default:
		return "other"
	}
}

func FeatureNames() []string {
	names := make([]string, 0, FeatureCount)
	for _, lag := range lags {
		for k := 0; k <= noneIndex; k++ {
			names = append(names, fmt.Sprintf("lag%d_%d", lag, k))
		}
	}
	for _, a := range ActionTypes {
		names = append(names, "lastk_"+string(a))
	}
	for _, a := range ActionTypes {
		names = append(names, "lastk12_"+string(a))
	}
	for _, a := range ActionTypes {
		names = append(names, "cum_"+string(a))
	}
	for _, a := range ActionTypes {
		names = append(names, "since_"+string(a))
	}
	names = append(names,
		"streak", "distinct_actions", "action_entropy", "frac_tool_result",
		"is_tool_result", "is_main_loop", "step_idx", "time_since_last",
		"time_since_start", "is_long_gap", "log_ctx_tokens", "prev_out",
		"prev2_out", "mean_out", "last_is_error",
	)
	for _, b := range extBuckets {
		names = append(names, "ext_"+b)
	}
	return names
}

func appendRing(r []Action, v Action, cap int) []Action {
	r = append(r, v)
	if len(r) > cap {
		r = r[len(r)-cap:]
	}
	return r
}

func appendIntRing(r []int, v, cap int) []int {
	r = append(r, v)
	if len(r) > cap {
		r = r[len(r)-cap:]
	}
	return r
}

func countAction(r []Action, a Action) int {
	n := 0
	for _, x := range r {
		if x == a {
			n++
		}
	}
	return n
}

func prevOut(r []int, k int) float64 {
	if len(r) < k {
		return 0.0
	}
	return log1p(float64(r[len(r)-k]))
}

func cumFrac(n, total int) float64 {
	if total <= 0 {
		return 0.0
	}
	return float64(n) / float64(total)
}

func log1p(x float64) float64 {
	if x < 0 {
		x = 0
	}
	return math.Log1p(x)
}

func b2f(b bool) float64 {
	if b {
		return 1.0
	}
	return 0.0
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func lower(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + 32
		}
	}
	return string(b)
}
