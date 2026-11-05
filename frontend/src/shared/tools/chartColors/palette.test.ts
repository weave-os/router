// @vitest-environment node

import {
  ACCOUNT_SERIES_COLORS,
  ACCOUNT_SERIES_COLOR_TOKENS,
  AccountChartColor,
  AccountChartColorToken,
  AiCodeAssistantColor,
  AiCodeAssistantColorToken,
  ChartContrastColor,
  ChartContrastColorToken,
  CustomAIAgentChartColor,
  CustomAIAgentChartColorToken,
  CUSTOM_AI_AGENT_COLORS,
  CUSTOM_AI_AGENT_COLOR_TOKENS,
  LOCTypeChartColor,
  LOCTypeChartColorToken,
  PRTypeChartColor,
  PRTypeChartColorToken,
  RankingChartColor,
  RankingChartColorToken,
  RouterModelFamilyChartColor,
  RouterModelFamilyChartColorToken,
  WeeklyInsightTierChartColor,
  WeeklyInsightTierChartColorToken,
  BENCHMARK_COLORS,
  BENCHMARK_COLOR_TOKENS,
  CHART_COLOR_DECLARATIONS,
  ChartColor,
  ChartColorToken,
  COMPONENT_COLORS,
  COMPONENT_COLOR_TOKENS,
  PIE_SERIES_COLORS,
  PIE_SERIES_COLOR_TOKENS,
  PROJECT_COLORS,
  PROJECT_COLOR_TOKENS,
  ProviderChartColor,
  ProviderChartColorToken,
  SERIES_COLORS,
  SERIES_COLOR_TOKENS,
} from ".";

const ORIGINAL_CHART_COLORS = {
  Benchmark1: "#41CAEC",
  Benchmark2: "#00A1C1",
  Benchmark3: "#009998",
  Benchmark4: "#007998",
  Benchmark5: "#005371",
  BenchmarkMedian: "#BE4040",
  BenchmarkP90: "#F7CD04",
  BenchmarkP99: "#8FD9A1",
  BenchmarkPreAI: "#9B8EC4",
  Black: "#0A0A0A",
  Blue: "#1D5C83",
  GoalLine: "#7b70c9",
  Green: "#3FB950",
  Orange1: "#EC6341",
  Orange2: "#EC9C41",
  Orange3: "#6E0000",
  Orange4: "#BE4040",
  Red: "#DA3633",
  Scale1: "#FF6C47",
  Scale2: "#1D5C83",
  Scale3: "#358CC2",
  Scale4: "#65AAD5",
  Scale5: "#ABCDE3",
  Trend: "#93C5FD",
  White: "#FFFFFF",
};
const ORIGINAL_ACCOUNT_COLORS = [
  "#4E79A7",
  "#F28E2B",
  "#59A14F",
  "#E15759",
  "#B07AA1",
  "#76B7B2",
  "#FF9DA7",
  "#9C755F",
  "#17BECF",
  "#BCBD22",
  "#1F77B4",
  "#EE6677",
  "#117733",
  "#6E4B9E",
  "#AA3377",
  "#7F7F7F",
  "#66CCEE",
  "#2CA02C",
  "#882255",
  "#1ABC9C",
  "#D62728",
  "#3498DB",
  "#C678DD",
  "#C49C94",
];
const ORIGINAL_PROVIDER_COLORS = {
  OpenAI: "#2a78d6",
  Anthropic: "#eb6834",
  Google: "#1baf7a",
  DeepSeek: "#eda100",
  Moonshot: "#e87ba4",
  MiniMax: "#008300",
  Qwen: "#4a3aa7",
  XAI: "#f43f5e",
  Meta: "#0866ff",
  Other: "#898781",
};
const ORIGINAL_SERIES_COLORS = [
  "#FF6C47",
  "#1D5C83",
  "#3FB950",
  "#7b70c9",
  "#EC9C41",
  "#41CAEC",
  "#DA3633",
  "#009998",
  "#F7CD04",
  "#358CC2",
];
const ORIGINAL_PIE_COLORS = ["#FF6C47", "#1D5C83", "#358CC2", "#65AAD5", "#ABCDE3"];
const ORIGINAL_BENCHMARK_COLORS = ["#41CAEC", "#00A1C1", "#009998", "#007998", "#005371"];
const ORIGINAL_COMPONENT_COLORS = [
  "#3FB950",
  "#1D5C83",
  "#EC6341",
  "#EC9C41",
  "#DA3633",
  "#358CC2",
  "#65AAD5",
  "#6E0000",
  "#BE4040",
];
const ORIGINAL_PROJECT_COLORS = [
  "#1D5C83",
  "#3FB950",
  "#358CC2",
  "#7b70c9",
  "#41CAEC",
  "#DA3633",
  "#F7CD04",
  "#EC6341",
  "#009998",
  "#EC9C41",
  "#9B8EC4",
  "#BE4040",
  "#8FD9A1",
  "#6E0000",
  "#00A1C1",
  "#65AAD5",
];

function resolveTokens(tokens: readonly string[]) {
  return tokens.map(token => CHART_COLOR_DECLARATIONS[token.slice(4, -1)]);
}

describe("canonical chart palettes", () => {
  it("retains the full literal enum, including both historical aliases", () => {
    expect(ChartColor).toEqual(ORIGINAL_CHART_COLORS);
    expect(Object.values(ChartColor)).toHaveLength(25);
    expect(new Set(Object.values(ChartColor)).size).toBe(23);
    expect(ChartColor.Blue).toBe("#1D5C83");
    expect(ChartColor.Scale2).toBe("#1D5C83");
    expect(resolveTokens([ChartColorToken.Blue, ChartColorToken.Scale2])).toEqual([
      "#1D5C83",
      "#1D5C83",
    ]);
    expect(resolveTokens([ChartColorToken.BenchmarkMedian, ChartColorToken.Orange4])).toEqual([
      "#BE4040",
      "#BE4040",
    ]);
  });

  it.each([
    ["general", SERIES_COLORS, SERIES_COLOR_TOKENS, ORIGINAL_SERIES_COLORS],
    ["account", ACCOUNT_SERIES_COLORS, ACCOUNT_SERIES_COLOR_TOKENS, ORIGINAL_ACCOUNT_COLORS],
    ["pie", PIE_SERIES_COLORS, PIE_SERIES_COLOR_TOKENS, ORIGINAL_PIE_COLORS],
    ["benchmark", BENCHMARK_COLORS, BENCHMARK_COLOR_TOKENS, ORIGINAL_BENCHMARK_COLORS],
    ["component", COMPONENT_COLORS, COMPONENT_COLOR_TOKENS, ORIGINAL_COMPONENT_COLORS],
    ["project", PROJECT_COLORS, PROJECT_COLOR_TOKENS, ORIGINAL_PROJECT_COLORS],
  ] as const)(
    "keeps every %s slot in original order in both views",
    (_name, literals, tokens, expected) => {
      expect(literals).toEqual(expected);
      expect(resolveTokens(tokens)).toEqual(expected);
    },
  );

  it("keeps the account enum and token order aligned to all 24 historical slots", () => {
    expect(Object.values(AccountChartColor)).toEqual(ORIGINAL_ACCOUNT_COLORS);
    expect(resolveTokens(Object.values(AccountChartColorToken))).toEqual(ORIGINAL_ACCOUNT_COLORS);
    expect(AccountChartColorToken.VividBlue).toBe("var(--wv-chart-account-vividblue)");
  });

  it("preserves all ten provider associations, including Other", () => {
    expect(ProviderChartColor).toEqual(ORIGINAL_PROVIDER_COLORS);
    expect(resolveTokens(Object.values(ProviderChartColorToken))).toEqual(
      Object.values(ORIGINAL_PROVIDER_COLORS),
    );
    expect(ProviderChartColorToken.Other).toBe("var(--wv-chart-provider-other)");
  });

  it("imports the public palette without a DOM", async () => {
    expect(typeof window).toBe("undefined");
    expect(typeof document).toBe("undefined");
    const palette = await import(".");
    expect(palette.ChartColor.Scale1).toBe("#FF6C47");
    expect(palette.ChartColorToken.Scale1).toBe("var(--wv-chart-scale1)");
  });
});

// Frozen from the pre-migration domain definitions, not generated from the current palette.
const ORIGINAL_AI_TOOL_COLORS = {
  AiGateway: "#0EA5E9",
  AmazonQ: "#FF9900",
  AuggieAutomation: "#0D9488",
  AuggieInteractive: "#84CC16",
  Augment: "#10B981",
  AugmentCodeReview: "#166534",
  ClaudeCode: "#d97757",
  Cline: "#24292F",
  CodeRabbit: "#FF570A",
  Codex: "#3a86ff",
  ContinueAI: "#D4130D",
  Cowork: "#1e3a8a",
  Cubic: "#a7a7a7",
  CursorAgent: "#555555",
  CursorBugBot: "#999999",
  CursorIDE: "#777777",
  Devin: "#45bfd7",
  Firebender: "#E53935",
  GeminiCLI: "#8A2BE2",
  GeminiCodeAssist: "#9168C0",
  GitHub: "#000000",
  GithubCopilot: "#2d09d7",
  Graphite: "#F5F5F5",
  Greptile: "#107a4d",
  Grok: "#0F766E",
  Hermes: "#7C3AED",
  JetbrainsAiAssistant: "#E17602",
  JetbrainsCodeCompletion: "#E17602",
  JetbrainsNes: "#E17602",
  Jules: "#b2a3ff",
  Junie: "#48E054",
  Kiro: "#00A5E0",
  Langfuse: "#FF5D5F",
  Modal: "#09AF58",
  Nori: "#065c0e",
  Openclaw: "#DC2626",
  Opencode: "#00C853",
  Openhands: "#E7A63E",
  Openrouter: "#6467F2",
  Parameter: "#3370FF",
  Pi: "#DB2777",
  Sazabi: "#9C1F2E",
  Sourcegraph: "#810f0c",
  Warp: "#0868F9",
  WeaveRouter: "#EC6341",
  Windsurf: "#34E8BB",
  Wispbit: "#E26A3B",
};
const ORIGINAL_MODEL_FAMILY_COLORS = {
  Anthropic1: "#C2410C",
  Anthropic2: "#EA580C",
  Anthropic3: "#F97316",
  Anthropic4: "#FB923C",
  Anthropic5: "#FDBA74",
  Google1: "#7E22CE",
  Google2: "#9333EA",
  Google3: "#A855F7",
  Google4: "#C084FC",
  Google5: "#D8B4FE",
  OpenAI1: "#1D4ED8",
  OpenAI2: "#2563EB",
  OpenAI3: "#3B82F6",
  OpenAI4: "#60A5FA",
  OpenAI5: "#93C5FD",
  OpenSource1: "#15803D",
  OpenSource2: "#16A34A",
  OpenSource3: "#22C55E",
  OpenSource4: "#4ADE80",
  OpenSource5: "#86EFAC",
  Unknown1: "#A16207",
  Unknown2: "#CA8A04",
  Unknown3: "#EAB308",
  Unknown4: "#FACC15",
  Unknown5: "#FDE047",
  XAI1: "#BE123C",
  XAI2: "#E11D48",
  XAI3: "#F43F5E",
  XAI4: "#FB7185",
  XAI5: "#FDA4AF",
};

test.each([
  ["AI tool identity", AiCodeAssistantColor, AiCodeAssistantColorToken, ORIGINAL_AI_TOOL_COLORS],
  [
    "model family shades",
    RouterModelFamilyChartColor,
    RouterModelFamilyChartColorToken,
    ORIGINAL_MODEL_FAMILY_COLORS,
  ],
  [
    "ranking",
    RankingChartColor,
    RankingChartColorToken,
    { Worst: "#ec9c41", Middle: "#fbec5d", Best: "#26a641" },
  ],
  [
    "weekly insight tier",
    WeeklyInsightTierChartColor,
    WeeklyInsightTierChartColorToken,
    { Power: "#7C3AED", Effective: "#3B82F6", Rising: "#14B8A6", GettingStarted: "#84CC16" },
  ],
  ["contrast", ChartContrastColor, ChartContrastColorToken, { Black: "#000000", White: "#FFFFFF" }],
  [
    "PR type",
    PRTypeChartColor,
    PRTypeChartColorToken,
    { Bug: "hsl(12 100% 64%)", Feature: "hsl(203 42% 46%)", Ktlo: "hsl(160 42% 46%)" },
  ],
  [
    "LOC type",
    LOCTypeChartColor,
    LOCTypeChartColorToken,
    { Ai: "hsl(160 42% 46%)", Codegen: "hsl(203 57% 48%)", Human: "hsl(12 82% 59%)" },
  ],
] as const)(
  "retains exact %s paints in both compatibility and rendering views",
  (_name, colors, tokens, expected) => {
    expect(colors).toEqual(expected);
    expect(resolveTokens(Object.values(tokens))).toEqual(Object.values(expected));
  },
);

test("retains custom agent order and distinguishes its blue casing and fallback gray", () => {
  const originalColors = [
    "#DA3633",
    "#3FB950",
    "#3A86FF",
    "#EC6341",
    "#8A2BE2",
    "#14B8A6",
    "#EC4899",
    "#F7CD04",
  ];
  expect(CUSTOM_AI_AGENT_COLORS).toEqual(originalColors);
  expect(resolveTokens(CUSTOM_AI_AGENT_COLOR_TOKENS)).toEqual(originalColors);
  expect(CustomAIAgentChartColor.Other).toBe("#808080");
  expect(resolveTokens([CustomAIAgentChartColorToken.Other])).toEqual(["#808080"]);
});
