package taskarm

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"slices"
	"strings"
	"unicode"

	"weave-os/router/internal/router"
)

const (
	labelPrefix           = "@cycle"
	labelDefinitionPrefix = "[@cycle]:"
	feedbackFooterPrefix  = "_Weave Router feedback:_ "
	repeatedLabel         = "="
	positionalFieldsLen   = 4
	bucketCount           = 10000
)

type Arm string

const (
	ArmControl   Arm = "control"
	ArmTreatment Arm = "treatment"
)

type Label struct {
	Goal string
	Task string
	Step string
}

type Subject struct {
	InstallationID string
	UserID         string
}

type Assignment struct {
	Arm        Arm
	Propensity float64
	TaskKey    string
	Step       string
}

func PreviousTurnLabel(messages []router.ConversationMessage) (Label, bool) {
	end := len(messages)
	for {
		closingIdx := closingMessage(messages[:end])
		if closingIdx < 0 {
			return Label{}, false
		}
		label, repeated, ok := parseLabel(messages[closingIdx].Text)
		if !ok {
			return Label{}, false
		}
		if !repeated {
			return label, true
		}
		end = closingIdx
	}
}

func closingMessage(messages []router.ConversationMessage) int {
	promptIdx := lastUserPrompt(messages)
	for i := len(messages) - 1; i > promptIdx; i-- {
		if _, isLabel := labelBody(lastLine(messages[i].Text)); messages[i].Role == "assistant" && isLabel {
			return i
		}
	}
	if promptIdx < 0 {
		return -1
	}
	return lastAssistantText(messages[:promptIdx])
}

func lastUserPrompt(messages []router.ConversationMessage) int {
	for i := len(messages) - 1; i >= 0; i-- {
		msg := messages[i]
		if msg.Role == "user" && len(msg.ToolResults) == 0 && strings.TrimSpace(msg.Text) != "" {
			return i
		}
	}
	return -1
}

func lastAssistantText(messages []router.ConversationMessage) int {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role != "assistant" {
			return -1
		}
		if strings.TrimSpace(messages[i].Text) != "" {
			return i
		}
	}
	return -1
}

func lastLine(text string) string {
	text = strings.TrimSpace(text)
	line := strings.TrimSpace(text[strings.LastIndexByte(text, '\n')+1:])
	if strings.HasPrefix(line, feedbackFooterPrefix) && len(line) < len(text) {
		return lastLine(text[:len(text)-len(line)])
	}
	return line
}

func labelBody(line string) (string, bool) {
	if body, found := strings.CutPrefix(line, labelDefinitionPrefix); found {
		body = strings.TrimSpace(body)
		body = strings.TrimSuffix(strings.TrimPrefix(body, "<"), ">")
		return strings.TrimSpace(body), true
	}
	rest, found := strings.CutPrefix(line, labelPrefix)
	if !found || (rest != "" && !unicode.IsSpace(rune(rest[0]))) {
		return "", false
	}
	return strings.TrimSpace(rest), true
}

func parseLabel(text string) (label Label, repeated bool, ok bool) {
	body, found := labelBody(lastLine(text))
	if !found {
		return Label{}, false, false
	}
	if strings.HasPrefix(body, "{") {
		var fields struct {
			Goal string `json:"goal"`
			Task string `json:"task"`
			Step string `json:"step"`
		}
		if json.Unmarshal([]byte(body), &fields) != nil {
			return Label{}, false, false
		}
		label = Label{Goal: strings.TrimSpace(fields.Goal), Task: strings.TrimSpace(fields.Task), Step: strings.TrimSpace(fields.Step)}
		return label, false, label.Task != "" && label.Step != ""
	}
	head, _, _ := strings.Cut(body, "|")
	fields := strings.Fields(head)
	if len(fields) == 1 && fields[0] == repeatedLabel {
		return Label{}, true, true
	}
	if len(fields) != positionalFieldsLen {
		return Label{}, false, false
	}
	return Label{Goal: fields[0], Task: fields[1], Step: fields[2]}, false, true
}

func Assign(subject Subject, label Label, steps []string, treatmentPct int) (Assignment, bool) {
	if treatmentPct <= 0 || treatmentPct > 100 || !slices.Contains(steps, label.Step) {
		return Assignment{}, false
	}
	hash := sha256.New()
	for _, part := range []string{subject.InstallationID, subject.UserID, label.Goal, label.Task} {
		hash.Write([]byte(part))
		hash.Write([]byte{0})
	}
	sum := hash.Sum(nil)
	treatmentShare := float64(treatmentPct) / 100
	assignment := Assignment{
		Arm:        ArmControl,
		Propensity: 1 - treatmentShare,
		TaskKey:    hex.EncodeToString(sum[8:24]),
		Step:       label.Step,
	}
	if binary.BigEndian.Uint64(sum[:8])%bucketCount < uint64(treatmentPct)*bucketCount/100 {
		assignment.Arm = ArmTreatment
		assignment.Propensity = treatmentShare
	}
	return assignment, true
}

func Steps(list string) []string {
	var steps []string
	for _, step := range strings.Split(list, ",") {
		if step = strings.TrimSpace(step); step != "" {
			steps = append(steps, step)
		}
	}
	return steps
}
