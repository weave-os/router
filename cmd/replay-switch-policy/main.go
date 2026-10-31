// Command replay-switch-policy evaluates frozen, content-free routing proposals.
// It performs no inference, database access, or serving configuration changes.
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"time"

	"weave-os/router/internal/router/escalation"
	"weave-os/router/internal/router/switchpolicy"
)

type record struct {
	ID         string                 `json:"id"`
	SessionKey string                 `json:"session_key"`
	Role       string                 `json:"role"`
	Timestamp  time.Time              `json:"timestamp"`
	Proposal   switchpolicy.Selection `json:"proposal"`
	Confidence *float64               `json:"confidence"`
	Eligible   *bool                  `json:"eligible"`
	Override   switchpolicy.Override  `json:"override,omitempty"`
	Observed   switchpolicy.Target    `json:"observed"`
}

type pinKey struct {
	Session string
	Role    string
}

type observationKey struct {
	Pin pinKey
	ID  string
}

type replayPin struct {
	State         switchpolicy.State
	Observed      switchpolicy.Target
	LastTimestamp time.Time
	Seen          bool
}

type targetCount struct {
	Target   switchpolicy.Target `json:"target"`
	Requests int                 `json:"requests"`
}

type summary struct {
	Version                  switchpolicy.Version        `json:"policy_version"`
	Config                   switchpolicy.Config         `json:"config"`
	InputSHA256              string                      `json:"input_sha256"`
	PinBuckets               int                         `json:"pin_buckets"`
	Observations             int                         `json:"observations"`
	ObservedTransitions      int                         `json:"observed_transitions"`
	CandidateTransitions     int                         `json:"candidate_transitions"`
	ChangedRequests          int                         `json:"changed_requests"`
	QualifiedHigherGroupHeld int                         `json:"qualified_higher_group_held"`
	CandidateCounts          []targetCount               `json:"candidate_counts"`
	Reasons                  map[switchpolicy.Reason]int `json:"reasons"`
	Limitations              []string                    `json:"limitations"`
}

type traceRecord struct {
	ID         string                `json:"id"`
	SessionKey string                `json:"session_key"`
	Role       string                `json:"role"`
	Timestamp  time.Time             `json:"timestamp"`
	Observed   switchpolicy.Target   `json:"observed"`
	Decision   switchpolicy.Decision `json:"decision"`
}

func replay(input io.Reader, trace io.Writer, config switchpolicy.Config) (summary, error) {
	hash := sha256.New()
	decoder := json.NewDecoder(io.TeeReader(input, hash))
	decoder.DisallowUnknownFields()
	encoder := json.NewEncoder(trace)
	pins := make(map[pinKey]replayPin)
	seen := make(map[observationKey]struct{})
	counts := make(map[switchpolicy.Target]int)
	output := summary{
		Version: switchpolicy.VersionAgreementV1, Config: config,
		Reasons: make(map[switchpolicy.Reason]int),
		Limitations: []string{
			"Every pin bucket starts empty with no incumbent or prior votes; mid-session suffixes cold-start instead of continuing the existing session.",
			"Classifier proposals and request timing are held fixed; alternative model outputs are not generated.",
			"Pin state is serialized in input order; distributed database interleavings are not reproduced.",
			"Input must supply request-compatible proposals and explicit recovery or intent overrides.",
			"Counts describe routing changes, not answer quality, monetary savings, or latency.",
			"This candidate is not wired into serving; durable revision-guarded persistence is a separate integration.",
		},
	}
	for {
		var row record
		if err := decoder.Decode(&row); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			return summary{}, fmt.Errorf("decode observation %d: %w", output.Observations+1, err)
		}
		if row.ID == "" || row.SessionKey == "" || row.Role == "" || row.Timestamp.IsZero() || row.Confidence == nil || row.Eligible == nil || row.Observed.Model == "" || row.Observed.Provider == "" {
			return summary{}, fmt.Errorf("observation %d is missing required routing metadata", output.Observations+1)
		}
		key := pinKey{Session: row.SessionKey, Role: row.Role}
		identity := observationKey{Pin: key, ID: row.ID}
		if _, duplicate := seen[identity]; duplicate {
			return summary{}, fmt.Errorf("observation %d repeats an observation id in the same pin bucket", output.Observations+1)
		}
		pin := pins[key]
		if row.Timestamp.Before(pin.LastTimestamp) {
			return summary{}, fmt.Errorf("observation %d is out of order within its pin bucket", output.Observations+1)
		}
		decision, err := switchpolicy.Evaluate(config, pin.State, switchpolicy.Observation{
			ID: row.ID, ExpectedRevision: pin.State.Revision, Proposal: row.Proposal,
			Confidence: *row.Confidence, Eligible: *row.Eligible, Override: row.Override,
		})
		if err != nil {
			return summary{}, fmt.Errorf("evaluate observation %d: %w", output.Observations+1, err)
		}
		pin.State, err = switchpolicy.Commit(pin.State, decision)
		if err != nil {
			return summary{}, fmt.Errorf("commit observation %d: %w", output.Observations+1, err)
		}
		if pin.Seen && pin.Observed != row.Observed {
			output.ObservedTransitions++
		}
		if decision.Switched {
			output.CandidateTransitions++
		}
		if pin.State.Current.Target != row.Observed {
			output.ChangedRequests++
		}
		if *row.Eligible && *row.Confidence >= config.MinimumConfidence && pin.State.Current.Target != row.Proposal.Target && escalation.Rank(pin.State.Current.Group) < escalation.Rank(row.Proposal.Group) {
			output.QualifiedHigherGroupHeld++
		}
		pin.Observed, pin.LastTimestamp, pin.Seen = row.Observed, row.Timestamp, true
		pins[key] = pin
		seen[identity] = struct{}{}
		counts[pin.State.Current.Target]++
		output.Reasons[decision.Reason]++
		output.Observations++
		if err := encoder.Encode(traceRecord{ID: row.ID, SessionKey: row.SessionKey, Role: row.Role, Timestamp: row.Timestamp, Observed: row.Observed, Decision: decision}); err != nil {
			return summary{}, fmt.Errorf("write replay trace: %w", err)
		}
	}
	if output.Observations == 0 {
		return summary{}, errors.New("input contains no observations")
	}
	output.PinBuckets = len(pins)
	output.InputSHA256 = hex.EncodeToString(hash.Sum(nil))
	for target, requests := range counts {
		output.CandidateCounts = append(output.CandidateCounts, targetCount{Target: target, Requests: requests})
	}
	sort.Slice(output.CandidateCounts, func(i, j int) bool {
		a, b := output.CandidateCounts[i].Target, output.CandidateCounts[j].Target
		if a.Provider != b.Provider {
			return a.Provider < b.Provider
		}
		return a.Model < b.Model
	})
	return output, nil
}

func run(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("replay-switch-policy", flag.ContinueOnError)
	flags.SetOutput(stderr)
	inputPath := flags.String("input", "", "Content-free JSONL routing observations (required)")
	tracePath := flags.String("trace", "", "New private JSONL decision trace (optional; never overwrites)")
	config := switchpolicy.Config{}
	flags.Float64Var(&config.MinimumConfidence, "minimum-confidence", .5, "Minimum qualifying probability for an ordinary switch")
	flags.IntVar(&config.UpgradeVotes, "upgrade-votes", 3, "Consecutive qualifying votes for the same higher-group destination")
	flags.IntVar(&config.DowngradeVotes, "downgrade-votes", 5, "Consecutive qualifying votes for the same lower/lateral destination")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *inputPath == "" || flags.NArg() != 0 {
		return errors.New("use -input PATH with no positional arguments")
	}
	if err := config.Validate(); err != nil {
		return err
	}
	input, err := os.Open(*inputPath)
	if err != nil {
		return fmt.Errorf("open replay input: %w", err)
	}
	defer input.Close()
	trace := io.Discard
	if *tracePath != "" {
		file, err := os.OpenFile(*tracePath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return fmt.Errorf("create private trace: %w", err)
		}
		defer file.Close()
		trace = file
	}
	output, err := replay(input, trace, config)
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(output)
}

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
