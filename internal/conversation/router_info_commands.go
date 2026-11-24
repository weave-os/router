package conversation

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"weave-os/router/internal/observability"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/translate"
)

const routingMarkerPrefix = "✦ **Weave Router** → "

type ForceableModel struct {
	ID, Provider  string
	Refused       bool
	RefusalReason string
	Shorthands    []string
}

func ListForceableModels(ctx context.Context, names map[string]string, binder ForceModelBinder) []ForceableModel {
	shorthands := make(map[string][]string)
	for name, id := range names {
		if name != id {
			shorthands[id] = append(shorthands[id], name)
		}
	}
	out := make([]ForceableModel, 0, len(catalog.Models))
	for _, m := range catalog.Models {
		_, reason, refused := binder.Binding(ctx, m.ID, m.PrimaryProvider())
		names := shorthands[m.ID]
		sort.Slice(names, func(i, j int) bool {
			if len(names[i]) != len(names[j]) {
				return len(names[i]) < len(names[j])
			}
			return names[i] < names[j]
		})
		out = append(out, ForceableModel{
			ID: m.ID, Provider: m.PrimaryProvider(), Refused: refused, RefusalReason: reason, Shorthands: names,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Provider != out[j].Provider {
			return out[i].Provider < out[j].Provider
		}
		return out[i].ID < out[j].ID
	})
	observability.FromContext(ctx).Debug("/list-models: answered from the force-model catalog",
		"models", len(out),
	)
	return out
}

const listModelsShorthandLimit = 4

func ListModelsMessage(models []ForceableModel, prefix string, format translate.Format) string {
	var b strings.Builder
	available := 0
	provider := ""
	for _, m := range models {
		if m.Provider != provider {
			provider = m.Provider
			b.WriteString("\n" + provider + "\n")
		}
		mark := "x"
		if m.Refused {
			mark = " "
		} else {
			available++
		}
		row := fmt.Sprintf("  [%s] %s", mark, m.ID)
		if names := m.Shorthands; len(names) > 0 {
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
