// Package cascade decides when a session should escalate from a cheap model to
// a stronger one, using the agent's own verification output rather than a
// prediction. Pure and I/O-free (mirrors the turntype/percallband packages); the
// per-session state store and the wiring live in internal/proxy.
//
// # Why this exists
//
// Measured offline against 1,907 graded SWE-bench cells, per-task
// complementarity is large: an oracle that knows which model suffices beats the
// best cost-matched mixture of fixed models by +10.4 to +17.6pp, and its quality
// exceeds every single model's mean, so no fixed choice and no weighted coin flip
// between fixed choices reaches it at any price.
//
// Predicting which turns need the strong model did not work. The only feature
// available before routing — repository identity, scored leave-one-out —
// captured -45%, -32%, -7% and +48% of that gain across four panels, every
// task-clustered bootstrap interval spanning zero.
//
// A cascade does not predict. It runs the cheap model, reads whether the tests
// passed, and escalates only when they did not. That captured 46-75% of the
// oracle's gain offline, and — the property that makes it deployable — it stayed
// positive as the verification signal was degraded, +7.3pp at 80% trigger
// accuracy and still +1.4pp at a coin flip. The design does not need the detector
// to be good, only better than nothing.
//
// The figures above come from an internal offline evaluation over public
// SWE-bench tasks; the write-up and the staged rollout plan live in the private
// WorkWeave repo rather than here.
package cascade

// EscalateAfterFailures is the number of *consecutive* failed verifications
// before a session escalates.
//
// Two, not one. One failure is the agent loop working as designed — write code,
// run tests, see a failure, fix it. Escalating on the first would send almost
// every session to the strong model and collapse the cascade into "always use
// the strong model" at strong-model prices, which is the baseline it has to beat.
const EscalateAfterFailures = 2

// MaxRungs caps how far a session can climb.
//
// Beyond three, the tail pays for a fourth full attempt to rescue a session that
// three models already failed; offline that was worse than the cost-matched
// frontier on every panel. A session that exhausts the ladder stays on the top
// rung rather than cycling.
const MaxRungs = 3

// Verdict is what verification said about the previous turn's work.
//
// NoSignal is a first-class outcome, not an error. Most turns carry no test
// output at all, and a turn whose output is not recognizably a test result must
// count as neither pass nor fail — that way an unparsed runner format degrades
// to "never escalate" (today's behaviour, the cheap model) rather than to
// "always escalate" (strong-model prices for everyone).
type Verdict string

const (
	NoSignal Verdict = "no_signal"
	Passed   Verdict = "passed"
	Failed   Verdict = "failed"
)

// State is one session's position in the ladder.
type State struct {
	// ConsecutiveFailures resets on a pass and is untouched by NoSignal.
	ConsecutiveFailures int
	// Escalated is sticky once set. See Observe.
	Escalated bool
	// Rung is the index into the Ladder this session is currently on.
	Rung int
}

// Observe folds one verdict into the state and returns the result.
//
// Escalation is **sticky**: once a session has needed the strong model it keeps
// it, and a subsequent passing test does not de-escalate. Two reasons, and the
// second is the load-bearing one. A session that needed help at turn 20 is a hard
// session, and its difficulty has not changed because one test went green. And
// de-escalating would oscillate — every switch forfeits the prompt-cache prefix,
// which is 94-98% of an agentic prompt on our measurements, so a policy that
// flips back and forth pays that repeatedly for an estimate that is not moving.
//
// A NoSignal turn is deliberately inert: it neither advances nor resets the
// counter. Otherwise a long stretch of non-test turns would silently forgive a
// session that is genuinely stuck.
func (s State) Observe(v Verdict) State {
	switch v {
	case Failed:
		s.ConsecutiveFailures++
		if s.ConsecutiveFailures >= EscalateAfterFailures {
			s.Escalated = true
		}
	case Passed:
		s.ConsecutiveFailures = 0
	case NoSignal:
		// Inert by design.
	}
	return s
}

// Rung is one step on the ladder: a concrete provider and model.
type Rung struct {
	Provider string
	Model    string
}

// Ladder is the escalation order, cheapest first.
//
// Membership is supplied by the caller, cheapest first, from a per-harness
// price/quality frontier computed offline. It is per-harness because harness fit is
// family-dependent — a confirmed, pre-registered interaction where Anthropic
// models lose 0.23 solve rate moving from Claude Code to OpenCode while OpenAI
// models gain 0.27 — so the right pair differs by scaffold.
type Ladder []Rung

// Select returns the rung a session in this state should use.
//
// Reports ok=false for an empty ladder rather than inventing a model, so a
// misconfiguration surfaces as "cascade unavailable" instead of silently routing
// somewhere. A ladder with one rung is legal and means the cascade is inert —
// which is the honest outcome when the gate leaves no stronger arm to climb to,
// as happened on one of the four offline panels.
func (l Ladder) Select(s State) (Rung, int, bool) {
	if len(l) == 0 {
		return Rung{}, 0, false
	}
	// The session's current rung is the FLOOR, not the starting point. Reading
	// only the Escalated flag here made stickiness silently fail: Advance clears
	// the flag once the step is taken, so the next Select fell back to rung 0 and
	// the session dropped to the cheap model on its very next turn. The flag means
	// "climb one from where you are", not "you are climbing".
	index := s.Rung
	if s.Escalated {
		index = s.Rung + 1
	}
	if cap := min(len(l), MaxRungs); index >= cap {
		index = cap - 1
	}
	return l[index], index, true
}

// Advance returns the state a session should carry forward after being served on
// rung `index`, clearing the escalation flag it just consumed.
//
// Separating this from Observe is what stops one escalation from climbing the
// whole ladder: the flag is a request to move up exactly one step, and it is
// spent when that step is taken. Two more failures are needed for the next.
func (s State) Advance(index int) State {
	if !s.Escalated {
		return s
	}
	s.Rung = index
	s.Escalated = false
	s.ConsecutiveFailures = 0
	return s
}
