package bandswap

import (
	"bufio"
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"math"

	"github.com/dmitryikh/leaves"
)

const (
	Large    = "large"
	Small    = "small"
	EmbedDim = 768
)

//go:embed artifacts/action_clf_lgbm_embonly.txt
var embeddedModel []byte

//go:embed artifacts/action_clf_classes.json
var embeddedClasses []byte

var bandMap = map[string]string{
	"planning":       Large,
	"learning":       Large,
	"providing_info": Small,
	"tool_call":      Small,
	"confirmation":   Small,
	"refinement":     Small,
}

type Classifier struct {
	model   *leaves.Ensemble
	classes []string
}

func New() (clf *Classifier, err error) {
	var cj struct {
		Classes []string `json:"classes"`
	}
	err = json.Unmarshal(embeddedClasses, &cj)
	if err != nil {
		return nil, fmt.Errorf("bandswap: parse classes: %w", err)
	}
	model, err := leaves.LGEnsembleFromReader(bufio.NewReader(bytes.NewReader(embeddedModel)), true)
	if err != nil {
		return nil, fmt.Errorf("bandswap: load lgbm booster: %w", err)
	}
	if model.NFeatures() != EmbedDim {
		return nil, fmt.Errorf("bandswap: booster features %d, want %d", model.NFeatures(), EmbedDim)
	}
	if model.NOutputGroups() != len(cj.Classes) {
		return nil, fmt.Errorf("bandswap: booster output groups %d != %d class labels", model.NOutputGroups(), len(cj.Classes))
	}
	for _, a := range cj.Classes {
		if _, ok := bandMap[a]; !ok {
			return nil, fmt.Errorf("bandswap: class %q absent from band map", a)
		}
	}
	return &Classifier{model: model, classes: cj.Classes}, nil
}

func (c *Classifier) PredictAction(emb []float32) (action string, ok bool) {
	if len(emb) != EmbedDim {
		return "", false
	}
	fvals := make([]float64, EmbedDim)
	for i, e := range emb {
		fvals[i] = float64(e)
	}
	preds := make([]float64, len(c.classes))
	if err := c.model.Predict(fvals, 0, preds); err != nil {
		return "", false
	}
	best := 0
	bestP := math.Inf(-1)
	for i, p := range preds {
		if p > bestP {
			bestP = p
			best = i
		}
	}
	return c.classes[best], true
}

func (c *Classifier) PredictBand(emb []float32) (action, band string, ok bool) {
	action, ok = c.PredictAction(emb)
	if !ok {
		return "", "", false
	}
	return action, Band(action), true
}

func Band(action string) string {
	if b, ok := bandMap[action]; ok {
		return b
	}
	return Large
}
