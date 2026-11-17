package taskarm

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"strings"

	"weave-os/router/internal/router"
)

const (
	labelPrefix    = "@cycle"
	repeatedLabel  = "="
	labelFieldsLen = 4
	bucketCount    = 10000
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

type Assignment struct {
	Arm        Arm
	Propensity float64
	TaskKey    string
	Step       string
}

func PreviousTurnLabel(messages []router.ConversationMessage) (Label, bool) {
	end := len(messages)
	for {
		closing := closingMessage(messages[:end])
		if closing < 0 {
			return Label{}, false
		}
		fields, ok := labelFields(messages[closing].Text)
		if !ok {
			return Label{}, false
		}
		if len(fields) == labelFieldsLen {
			return Label{Goal: fields[0], Task: fields[1], Step: fields[2]}, true
		}
		end = closing
	}
}

func closingMessage(messages []router.ConversationMessage) int {
	prompt := lastUserPrompt(messages)
	for i := len(messages) - 1; i > prompt; i-- {
		if messages[i].Role == "assistant" && strings.HasPrefix(lastLine(messages[i].Text), labelPrefix) {
			return i
		}
	}
	if prompt < 0 {
		return -1
	}
	return lastAssistantText(messages[:prompt])
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
	return strings.TrimSpace(text[strings.LastIndexByte(text, '\n')+1:])
}

func labelFields(text string) ([]string, bool) {
	rest, found := strings.CutPrefix(lastLine(text), labelPrefix)
	if !found {
		return nil, false
	}
	head, _, _ := strings.Cut(rest, "|")
	fields := strings.Fields(head)
	if len(fields) == labelFieldsLen || (len(fields) == 1 && fields[0] == repeatedLabel) {
		return fields, true
	}
	return nil, false
}

func Assign(sessionKey []byte, label Label, steps []string, treatmentPct int) (Assignment, bool) {
	if treatmentPct <= 0 || treatmentPct > 100 || !contains(steps, label.Step) {
		return Assignment{}, false
	}
	hash := sha256.New()
	hash.Write(sessionKey)
	hash.Write([]byte{0})
	hash.Write([]byte(label.Goal))
	hash.Write([]byte{0})
	hash.Write([]byte(label.Task))
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

func contains(values []string, value string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}
