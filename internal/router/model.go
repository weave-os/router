package router

import (
	"fmt"
	"regexp"
)

type ModelCapability string

const (
	CapAdaptiveThinking          ModelCapability = "adaptive_thinking"
	CapExtendedThinking          ModelCapability = "extended_thinking"
	CapReasoning                 ModelCapability = "reasoning"
	CapExtendedContext           ModelCapability = "extended_context"
	CapXhighEffort               ModelCapability = "xhigh_effort"
	CapServerSideFallback        ModelCapability = "server_side_fallback"
	CapAutoToolChoiceOnly        ModelCapability = "auto_tool_choice_only"
	CapSamplingParamsUnsupported ModelCapability = "sampling_params_unsupported"
	CapAssistantPrefill          ModelCapability = "assistant_prefill"
)

const (
	ModelGemini4Argon = "gemini-4-argon"
	ModelGemini4Neon  = "gemini-4-neon"
)

type ModelSpec struct {
	capabilities map[ModelCapability]struct{}
	reasoning    ReasoningCapabilities
}

type ThinkingOff int

const (
	ThinkingOffDisabled ThinkingOff = iota
	ThinkingOffBetweenTools
	ThinkingOffAdaptiveLowEffort
)

type ReasoningCapabilities struct {
	Levels         []string
	SupportsAuto   bool
	SupportsBudget bool
	AlwaysOn       bool
	ThinkingOff    ThinkingOff
}

func (s ModelSpec) Supports(cap ModelCapability) bool {
	_, ok := s.capabilities[cap]
	return ok
}

func NewSpec(caps ...ModelCapability) ModelSpec {
	s := ModelSpec{capabilities: make(map[ModelCapability]struct{}, len(caps))}
	for _, c := range caps {
		s.capabilities[c] = struct{}{}
	}
	return s
}

func NewSpecWithReasoning(reasoning ReasoningCapabilities, caps ...ModelCapability) ModelSpec {
	s := NewSpec(caps...)
	s.reasoning = reasoning
	return s
}

func (s ModelSpec) Reasoning() ReasoningCapabilities {
	levels := append([]string(nil), s.reasoning.Levels...)
	return ReasoningCapabilities{
		Levels: levels, SupportsAuto: s.reasoning.SupportsAuto,
		SupportsBudget: s.reasoning.SupportsBudget, AlwaysOn: s.reasoning.AlwaysOn,
		ThinkingOff: s.reasoning.ThinkingOff,
	}
}

func (s ModelSpec) ValidateReasoningCapabilities() error {
	if len(s.reasoning.Levels) == 0 {
		if s.reasoning.SupportsAuto || s.reasoning.SupportsBudget || s.reasoning.AlwaysOn {
			return fmt.Errorf("reasoning modifiers require at least one supported level")
		}
		return nil
	}
	seen := make(map[string]struct{}, len(s.reasoning.Levels))
	for _, level := range s.reasoning.Levels {
		if level == "" {
			return fmt.Errorf("reasoning level cannot be empty")
		}
		if _, exists := seen[level]; exists {
			return fmt.Errorf("duplicate reasoning level %q", level)
		}
		seen[level] = struct{}{}
	}
	return nil
}

var dateSuffix = regexp.MustCompile(`-\d{4}-?\d{2}-?\d{2}$`)

func StripDateSuffix(model string) string {
	return dateSuffix.ReplaceAllString(model, "")
}

func Lookup(model string) ModelSpec {
	if s, ok := registry[model]; ok {
		return s
	}
	if base := StripDateSuffix(model); base != model {
		if s, ok := registry[base]; ok {
			return s
		}
	}
	return ModelSpec{}
}

var (
	anthropicAdaptive                  = NewSpecWithReasoning(ReasoningCapabilities{Levels: []string{"low", "medium", "high", "max"}, AlwaysOn: true}, CapAdaptiveThinking, CapExtendedContext)
	anthropicAdaptiveXhigh             = NewSpecWithReasoning(ReasoningCapabilities{Levels: []string{"low", "medium", "high", "max", "xhigh"}, AlwaysOn: true}, CapAdaptiveThinking, CapExtendedContext, CapXhighEffort)
	anthropicAdaptiveFallback          = NewSpecWithReasoning(ReasoningCapabilities{Levels: []string{"low", "medium", "high", "max", "xhigh"}, AlwaysOn: true}, CapAdaptiveThinking, CapExtendedContext, CapXhighEffort, CapServerSideFallback)
	anthropicAdaptiveFallbackAutoTools = NewSpecWithReasoning(ReasoningCapabilities{Levels: []string{"low", "medium", "high", "max", "xhigh"}, AlwaysOn: true, ThinkingOff: ThinkingOffBetweenTools}, CapAdaptiveThinking, CapExtendedContext, CapXhighEffort, CapServerSideFallback, CapAutoToolChoiceOnly)
	anthropicExtended                  = NewSpecWithReasoning(ReasoningCapabilities{Levels: []string{"low", "medium", "high"}, SupportsBudget: true}, CapExtendedThinking, CapAssistantPrefill)

	anthropicFable5  = NewSpecWithReasoning(ReasoningCapabilities{Levels: []string{"low", "medium", "high", "max", "xhigh"}, AlwaysOn: true, ThinkingOff: ThinkingOffAdaptiveLowEffort}, CapAdaptiveThinking, CapExtendedContext, CapXhighEffort, CapServerSideFallback)
	anthropicFable51 = NewSpecWithReasoning(ReasoningCapabilities{Levels: []string{"low", "medium", "high", "max", "xhigh"}, AlwaysOn: true, ThinkingOff: ThinkingOffAdaptiveLowEffort}, CapAdaptiveThinking, CapExtendedContext, CapXhighEffort, CapServerSideFallback, CapAutoToolChoiceOnly, CapSamplingParamsUnsupported)
)

var (
	openaiReasoning = NewSpecWithReasoning(ReasoningCapabilities{Levels: []string{"low", "medium", "high"}, SupportsBudget: true}, CapReasoning)
	openaiBase      = NewSpec()
	openai56        = NewSpecWithReasoning(ReasoningCapabilities{Levels: []string{"low", "medium", "high", "xhigh"}, SupportsBudget: true}, CapReasoning, CapXhighEffort)
	openaiAstra     = NewSpecWithReasoning(ReasoningCapabilities{Levels: []string{"low", "medium", "high", "xhigh", "max"}, SupportsBudget: true, AlwaysOn: true}, CapReasoning, CapXhighEffort)
	openai6         = NewSpecWithReasoning(ReasoningCapabilities{Levels: []string{"low", "medium", "high", "xhigh", "max"}, SupportsBudget: true}, CapReasoning, CapXhighEffort)
	openai61        = NewSpecWithReasoning(ReasoningCapabilities{Levels: []string{"low", "medium", "high", "xhigh", "max"}, SupportsBudget: true, AlwaysOn: true}, CapReasoning, CapXhighEffort)
	grok46          = NewSpecWithReasoning(ReasoningCapabilities{Levels: []string{"low", "medium", "high", "xhigh"}, SupportsBudget: true}, CapReasoning, CapXhighEffort)
	grok47          = NewSpecWithReasoning(ReasoningCapabilities{Levels: []string{"low", "medium", "high", "xhigh"}, SupportsBudget: true}, CapReasoning, CapXhighEffort)
	museSpark       = NewSpecWithReasoning(ReasoningCapabilities{Levels: []string{"low", "medium", "high", "xhigh"}, SupportsBudget: true, AlwaysOn: true}, CapReasoning, CapXhighEffort)
	museMax         = NewSpecWithReasoning(ReasoningCapabilities{Levels: []string{"low", "medium", "high", "xhigh", "max"}, SupportsBudget: true, AlwaysOn: true}, CapReasoning, CapXhighEffort)
)

var minimaxM31Flash = NewSpecWithReasoning(ReasoningCapabilities{Levels: []string{"low", "medium", "high", "xhigh", "max"}, AlwaysOn: true}, CapReasoning, CapXhighEffort)

var googleBase = NewSpecWithReasoning(ReasoningCapabilities{Levels: []string{"low", "medium", "high"}, SupportsBudget: true})
var google3Base = NewSpecWithReasoning(ReasoningCapabilities{Levels: []string{"low", "medium", "high"}, AlwaysOn: true})
var google4Xhigh = NewSpecWithReasoning(ReasoningCapabilities{Levels: []string{"low", "medium", "high", "xhigh"}, AlwaysOn: true}, CapXhighEffort)

var openAICompatBase = NewSpec()

var registry = map[string]ModelSpec{
	"claude-fable-5":    anthropicFable5,
	"claude-fable-5-1":  anthropicFable51,
	"claude-fable-5-2":  anthropicFable51,
	"claude-mythos-5-2": anthropicFable51,
	"claude-opus-5-5": NewSpecWithReasoning(
		ReasoningCapabilities{Levels: []string{"low", "medium", "high", "max", "xhigh"}, AlwaysOn: true, ThinkingOff: ThinkingOffAdaptiveLowEffort},
		CapAdaptiveThinking, CapExtendedContext, CapXhighEffort, CapServerSideFallback,
		CapAutoToolChoiceOnly,
	),
	"claude-sonnet-5-5": anthropicAdaptiveFallbackAutoTools,
	"claude-opus-5":     anthropicAdaptiveFallback,
	"claude-opus-4-8":   anthropicAdaptiveXhigh,
	"claude-opus-4-7":   anthropicAdaptiveXhigh,
	"claude-sonnet-5":   anthropicAdaptive,
	"claude-sonnet-4-6": anthropicAdaptive,
	"claude-opus-4-6":   anthropicAdaptive,

	"claude-haiku-4-5": anthropicExtended,
	"claude-opus-4-5":  anthropicExtended,
	"claude-opus-4-1":  anthropicExtended,
	"claude-opus-4-0":  anthropicExtended,

	"claude-haiku-5-5": NewSpecWithReasoning(
		ReasoningCapabilities{Levels: []string{"low", "medium", "high", "max", "xhigh"}},
		CapAdaptiveThinking,
		CapExtendedContext,
		CapXhighEffort,
		CapServerSideFallback,
	),

	"claude-sonnet-4-5": NewSpec(CapAssistantPrefill),
	"claude-sonnet-4-0": NewSpec(CapAssistantPrefill),

	"gpt-6-astra":     openaiAstra,
	"gpt-6-sol":       openai6,
	"gpt-6.1-sol":     openai61,
	"gpt-6.1-terra":   openai61,
	"gpt-6.2-sol":     openai61,
	"gpt-6.2-astra":   openaiAstra,
	"gpt-6-luna":      openai6,
	ModelGemini4Argon: google3Base,
	ModelGemini4Neon:  google4Xhigh,

	"gpt-5.6-sol":      openai56,
	"gpt-5.6-sol-pro":  openai56,
	"gpt-5.6-terra":    openai56,
	"gpt-5.6-luna":     openai56,
	"gpt-5.6-luna-pro": openai56,

	"gpt-5.5":      openaiReasoning,
	"gpt-5.5-pro":  openaiReasoning,
	"gpt-5.5-mini": openaiReasoning,
	"gpt-5.5-nano": openaiReasoning,

	"grok-4.5": openaiReasoning,

	"grok-4.6": grok46,
	"grok-4.7": grok47,

	"muse-spark-1.3":       museSpark,
	"meta/muse-spark-open": museSpark,
	"muse-ember-1.0":       museMax,
	"muse-wonder-1.0":      museMax,

	"gpt-5.4":      openaiReasoning,
	"gpt-5.4-pro":  NewSpecWithReasoning(ReasoningCapabilities{Levels: []string{"medium", "high", "xhigh"}, AlwaysOn: true}, CapReasoning, CapXhighEffort),
	"gpt-5.4-mini": openaiReasoning,
	"gpt-5.4-nano": openaiReasoning,

	"gpt-5.3":     openaiReasoning,
	"gpt-5.2":     openaiReasoning,
	"gpt-5.2-pro": openaiReasoning,
	"gpt-5.1":     openaiReasoning,
	"gpt-5":       openaiReasoning,
	"gpt-5-chat":  openaiReasoning,
	"gpt-5-pro":   openaiReasoning,
	"gpt-5-mini":  openaiReasoning,
	"gpt-5-nano":  openaiReasoning,

	"o3":      openaiReasoning,
	"o3-pro":  openaiReasoning,
	"o3-mini": openaiReasoning,
	"o4-mini": openaiReasoning,
	"o1":      openaiReasoning,
	"o1-pro":  openaiReasoning,
	"o1-mini": openaiReasoning,

	"gpt-4.1":      openaiBase,
	"gpt-4.1-mini": openaiBase,
	"gpt-4.1-nano": openaiBase,
	"gpt-4o":       openaiBase,
	"gpt-4o-mini":  openaiBase,
	"gpt-4-turbo":  openaiBase,
	"gpt-4":        openaiBase,

	"gemini-3-pro-preview":          google3Base,
	"gemini-3.1-pro-preview":        google3Base,
	"gemini-3-flash-preview":        google3Base,
	"gemini-3.1-flash-lite-preview": google3Base,
	"gemini-3.1-flash-live-preview": google3Base,
	"gemini-3.5-flash":              google3Base,
	"gemini-3.5-flash-lite":         google3Base,
	"gemini-3.6-flash":              google3Base,
	"gemini-3.7-flash":              google3Base,
	"gemini-3.8-flash":              google3Base,

	"gemini-2.5-pro":        googleBase,
	"gemini-2.5-flash":      googleBase,
	"gemini-2.5-flash-lite": googleBase,
	"gemini-2.0-flash":      googleBase,
	"gemini-2.0-flash-lite": googleBase,

	"qwen/qwen3-235b-a22b-2507":        openAICompatBase,
	"qwen/qwen3-30b-a3b-instruct-2507": openAICompatBase,
	"qwen/qwen3-coder-next":            openAICompatBase,
	"qwen/qwen3-next-80b-a3b-instruct": openAICompatBase,
	"qwen/qwen3.8-max":                 openAICompatBase,

	"minimax/minimax-m3.1-flash": minimaxM31Flash,
}

func ValidateCatalogReasoningCapabilities() error {
	for model, spec := range registry {
		if err := spec.ValidateReasoningCapabilities(); err != nil {
			return fmt.Errorf("model %q: %w", model, err)
		}
	}
	return nil
}
