package proxy

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"weave-os/router/internal/observability"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/translate"
)

// forceableModel is one /force-model target: its catalog ID, the shorthands
// that resolve to it, and why this request's policy refuses it ("" if not).
type forceableModel struct {
	id, provider, refusal string
	shorthands            []string
}

// handleListModelsCommand answers a bare list-models directive synthetically,
// with no upstream call. Each model is judged by forcedModelBinding, the same
// check /force-model applies, so the listing cannot promise a force that would
// then be refused.
func (s *Service) handleListModelsCommand(
	ctx context.Context,
	w http.ResponseWriter,
	env *translate.RequestEnvelope,
	inputTokens int,
) error {
	models := s.forceableModels(ctx)
	observability.FromContext(ctx).Debug("/list-models: answered from the force-model catalog",
		"models", len(models),
	)
	ack := newCommandAcknowledgment(translate.CommandListModels)
	ack.forceable = models
	ack.clientApp = ClientIdentityFrom(ctx).ClientApp
	return writeCommandAcknowledgment(ctx, w, env, ack, inputTokens)
}

func (s *Service) forceableModels(ctx context.Context) []forceableModel {
	shorthands := make(map[string][]string)
	for name, id := range forceModelNames() {
		if name != id {
			shorthands[id] = append(shorthands[id], name)
		}
	}
	out := make([]forceableModel, 0, len(catalog.Models))
	for _, m := range catalog.Models {
		_, refusal := s.forcedModelBinding(ctx, m.ID, m.PrimaryProvider())
		names := shorthands[m.ID]
		sort.Slice(names, func(i, j int) bool {
			if len(names[i]) != len(names[j]) {
				return len(names[i]) < len(names[j])
			}
			return names[i] < names[j]
		})
		out = append(out, forceableModel{
			id: m.ID, provider: m.PrimaryProvider(), refusal: refusal, shorthands: names,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].provider != out[j].provider {
			return out[i].provider < out[j].provider
		}
		return out[i].id < out[j].id
	})
	return out
}

// listModelsShorthandLimit keeps a row on one line: the folded spellings
// ("opus-5.5", "opus55", ...) all still resolve, so listing the shortest few
// is enough to show what can be typed.
const listModelsShorthandLimit = 4

func listModelsMessage(models []forceableModel, prefix string, format translate.Format) string {
	var b strings.Builder
	available := 0
	provider := ""
	for _, m := range models {
		if m.provider != provider {
			provider = m.provider
			b.WriteString("\n" + provider + "\n")
		}
		mark := "x"
		if m.refusal != "" {
			mark = " "
		} else {
			available++
		}
		row := fmt.Sprintf("  [%s] %s", mark, m.id)
		if names := m.shorthands; len(names) > 0 {
			if len(names) > listModelsShorthandLimit {
				names = names[:listModelsShorthandLimit]
			}
			row += " (" + strings.Join(names, ", ") + ")"
		}
		b.WriteString(row + "\n")
	}
	footer := fmt.Sprintf(
		"\n%d of %d can be forced here. Pin one with `%sforce-model <id or shorthand>`; add `:low`, `:medium`, `:high` or `:max` to set effort.",
		available, len(models), prefix)
	if available < len(models) {
		footer += "\n[ ] models are refused by this installation's model or provider policy."
	}
	if format == translate.FormatOpenAI {
		return fmt.Sprintf("Weave Router: models you can force.\n%s%s", b.String(), footer)
	}
	return fmt.Sprintf("%smodels you can force\n```\n%s```\n%s\n\n", routingMarkerPrefix, b.String(), footer)
}
