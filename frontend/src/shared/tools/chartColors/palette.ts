export enum ChartColor {
  Benchmark1 = "#41CAEC",
  Benchmark2 = "#00A1C1",
  Benchmark3 = "#009998",
  Benchmark4 = "#007998",
  Benchmark5 = "#005371",

  // oxlint-disable-next-line typescript/no-duplicate-enum-values -- Orange4 intentionally reuses the benchmark median color
  BenchmarkMedian = "#BE4040",
  BenchmarkP90 = "#F7CD04",
  BenchmarkP99 = "#8FD9A1",
  BenchmarkPreAI = "#9B8EC4",
  Black = "#0A0A0A",
  // oxlint-disable-next-line typescript/no-duplicate-enum-values -- Scale2 intentionally reuses the blue palette color
  Blue = "#1D5C83",
  GoalLine = "#7b70c9",
  Green = "#3FB950",
  Orange1 = "#EC6341",
  Orange2 = "#EC9C41",
  Orange3 = "#6E0000",
  Orange4 = "#BE4040",
  Red = "#DA3633",
  Scale1 = "#FF6C47",
  Scale2 = "#1D5C83",
  Scale3 = "#358CC2",
  Scale4 = "#65AAD5",
  Scale5 = "#ABCDE3",
  Trend = "#93C5FD",
  White = "#FFFFFF",
}

export enum AccountChartColor {
  Blue = "#4E79A7",
  Orange = "#F28E2B",
  Green = "#59A14F",
  Red = "#E15759",
  Purple = "#B07AA1",
  Teal = "#76B7B2",
  Pink = "#FF9DA7",
  Brown = "#9C755F",
  Cyan = "#17BECF",
  Olive = "#BCBD22",
  VividBlue = "#1F77B4",
  Coral = "#EE6677",
  Forest = "#117733",
  Violet = "#6E4B9E",
  Plum = "#AA3377",
  Gray = "#7F7F7F",
  Sky = "#66CCEE",
  LimeGreen = "#2CA02C",
  Wine = "#882255",
  Mint = "#1ABC9C",
  Vermillion = "#D62728",
  Azure = "#3498DB",
  Lavender = "#C678DD",
  Taupe = "#C49C94",
}

export enum ProviderChartColor {
  OpenAI = "#2a78d6",
  Anthropic = "#eb6834",
  Google = "#1baf7a",
  DeepSeek = "#eda100",
  Moonshot = "#e87ba4",
  MiniMax = "#008300",
  Qwen = "#4a3aa7",
  XAI = "#f43f5e",
  Meta = "#0866ff",
  Other = "#898781",
}

export enum AiCodeAssistantColor {
  AiGateway = "#0EA5E9", // sky blue for generic AI gateways

  AmazonQ = "#FF9900", // AWS Orange

  AuggieAutomation = "#0D9488",

  AuggieInteractive = "#84CC16",
  Augment = "#10B981",

  AugmentCodeReview = "#166534",

  ClaudeCode = "#d97757",

  Cline = "#24292F", // Cline's brand color (dark gray from their logo)

  CodeRabbit = "#FF570A",

  Codex = "#3a86ff", // Qubit Blue from Codex branding

  ContinueAI = "#D4130D", // Continue brand red

  Cowork = "#1e3a8a", // Cowork brand blue

  Cubic = "#a7a7a7",
  CursorAgent = "#555555",

  CursorBugBot = "#999999",

  CursorIDE = "#777777",

  Devin = "#45bfd7",

  Firebender = "#E53935", // Red for Firebender

  GeminiCLI = "#8A2BE2", // Blue Violet for Gemini

  GeminiCodeAssist = "#9168C0", // Gemini purple (from brand gradient)
  GitHub = "#000000",

  GithubCopilot = "#2d09d7",

  Graphite = "#F5F5F5",

  Greptile = "#107a4d",
  Grok = "#0F766E", // xAI's mark is monochrome; a deep teal keeps Grok distinct on charts

  /* oxlint-disable typescript/no-duplicate-enum-values -- JetBrains tools share brand color */
  Hermes = "#7C3AED", // Nous Research purple
  JetbrainsAiAssistant = "#E17602", // JetBrains orange
  JetbrainsCodeCompletion = "#E17602",
  JetbrainsNes = "#E17602",
  /* oxlint-enable typescript/no-duplicate-enum-values */

  Jules = "#b2a3ff",

  Junie = "#48E054",

  Kiro = "#00A5E0", // AWS Blue for Kiro

  Langfuse = "#FF5D5F", // Langfuse logo coral

  Modal = "#09AF58",

  Nori = "#065c0e", // Nori brand green

  Openclaw = "#DC2626",

  Opencode = "#00C853", // Green for Opencode

  Openhands = "#E7A63E",

  Openrouter = "#6467F2", // OpenRouter's mark is monochrome; its indigo accent keeps it distinct on charts

  Parameter = "#3370FF",

  Pi = "#DB2777", // Pi's own mark is monochrome; pink keeps it distinct on charts

  Sazabi = "#9C1F2E", // Sazabi crimson

  Sourcegraph = "#810f0c",

  Warp = "#0868F9", // Warp brand blue

  WeaveRouter = "#EC6341", // Weave brand orange

  Windsurf = "#34E8BB",

  Wispbit = "#E26A3B",
}

export enum CustomAIAgentChartColor {
  Red = "#DA3633",
  Green = "#3FB950",
  Blue = "#3A86FF",
  Orange = "#EC6341",
  Purple = "#8A2BE2",
  Teal = "#14B8A6",
  Pink = "#EC4899",
  Yellow = "#F7CD04",
  Other = "#808080",
}

export enum RouterModelFamilyChartColor {
  Anthropic1 = "#C2410C",
  Anthropic2 = "#EA580C",
  Anthropic3 = "#F97316",
  Anthropic4 = "#FB923C",
  Anthropic5 = "#FDBA74",
  Google1 = "#7E22CE",
  Google2 = "#9333EA",
  Google3 = "#A855F7",
  Google4 = "#C084FC",
  Google5 = "#D8B4FE",
  OpenAI1 = "#1D4ED8",
  OpenAI2 = "#2563EB",
  OpenAI3 = "#3B82F6",
  OpenAI4 = "#60A5FA",
  OpenAI5 = "#93C5FD",
  OpenSource1 = "#15803D",
  OpenSource2 = "#16A34A",
  OpenSource3 = "#22C55E",
  OpenSource4 = "#4ADE80",
  OpenSource5 = "#86EFAC",
  Unknown1 = "#A16207",
  Unknown2 = "#CA8A04",
  Unknown3 = "#EAB308",
  Unknown4 = "#FACC15",
  Unknown5 = "#FDE047",
  XAI1 = "#BE123C",
  XAI2 = "#E11D48",
  XAI3 = "#F43F5E",
  XAI4 = "#FB7185",
  XAI5 = "#FDA4AF",
}

export enum ChartContrastColor {
  Black = "#000000",
  White = "#FFFFFF",
}

export enum RankingChartColor {
  Worst = "#ec9c41",
  Middle = "#fbec5d",
  Best = "#26a641",
}

// Viridis-ordered (violet → blue → teal → lime) so the ladder reads as ordered in both themes.
export enum WeeklyInsightTierChartColor {
  Power = "#7C3AED",
  Effective = "#3B82F6",
  Rising = "#14B8A6",
  GettingStarted = "#84CC16",
}

export enum PRTypeChartHSLColor {
  Bug = "12 100% 64%",
  Feature = "203 42% 46%",
  Ktlo = "160 42% 46%",
}

export const LOCTypeChartHSLColor = {
  Ai: PRTypeChartHSLColor.Ktlo,
  Codegen: "203 57% 48%",
  Human: "12 82% 59%",
} as const;

export const PRTypeChartColor = Object.fromEntries(
  Object.entries(PRTypeChartHSLColor).map(([name, hsl]) => [name, `hsl(${hsl})`]),
) as Record<keyof typeof PRTypeChartHSLColor, string>;
export const LOCTypeChartColor = Object.fromEntries(
  Object.entries(LOCTypeChartHSLColor).map(([name, hsl]) => [name, `hsl(${hsl})`]),
) as Record<keyof typeof LOCTypeChartHSLColor, string>;

function createColorTokens<Key extends string, Prefix extends string>(
  colors: Record<Key, string>,
  prefix: Prefix,
): Record<Key, `var(--wv-chart-${Prefix}${Lowercase<Key>})`> {
  return Object.fromEntries(
    Object.keys(colors).map(name => [name, `var(--wv-chart-${prefix}${name.toLowerCase()})`]),
  ) as Record<Key, `var(--wv-chart-${Prefix}${Lowercase<Key>})`>;
}

export const ChartColorToken = createColorTokens(ChartColor, "");
export type ChartColorToken = (typeof ChartColorToken)[keyof typeof ChartColorToken];
export const AccountChartColorToken = createColorTokens(AccountChartColor, "account-");
export type AccountChartColorToken =
  (typeof AccountChartColorToken)[keyof typeof AccountChartColorToken];
export const ProviderChartColorToken = createColorTokens(ProviderChartColor, "provider-");
export type ProviderChartColorToken =
  (typeof ProviderChartColorToken)[keyof typeof ProviderChartColorToken];

export const AiCodeAssistantColorToken = createColorTokens(AiCodeAssistantColor, "ai-tool-");
export const CustomAIAgentChartColorToken = createColorTokens(
  CustomAIAgentChartColor,
  "custom-ai-",
);
export const RouterModelFamilyChartColorToken = createColorTokens(
  RouterModelFamilyChartColor,
  "model-family-",
);
export const ChartContrastColorToken = createColorTokens(ChartContrastColor, "contrast-");
export const RankingChartColorToken = createColorTokens(RankingChartColor, "ranking-");
export const WeeklyInsightTierChartColorToken = createColorTokens(
  WeeklyInsightTierChartColor,
  "tier-",
);
export const PRTypeChartColorToken = createColorTokens(PRTypeChartColor, "pr-type-");
export const LOCTypeChartColorToken = createColorTokens(LOCTypeChartColor, "loc-type-");

const CUSTOM_AI_AGENT_COLOR_SLOTS = [
  "Red",
  "Green",
  "Blue",
  "Orange",
  "Purple",
  "Teal",
  "Pink",
  "Yellow",
] as const satisfies readonly (keyof typeof CustomAIAgentChartColor)[];
export const CUSTOM_AI_AGENT_COLORS: readonly string[] = CUSTOM_AI_AGENT_COLOR_SLOTS.map(
  name => CustomAIAgentChartColor[name],
);
export const CUSTOM_AI_AGENT_COLOR_TOKENS: readonly string[] = CUSTOM_AI_AGENT_COLOR_SLOTS.map(
  name => CustomAIAgentChartColorToken[name],
);

const SERIES_COLOR_SLOTS = [
  "Scale1",
  "Scale2",
  "Green",
  "GoalLine",
  "Orange2",
  "Benchmark1",
  "Red",
  "Benchmark3",
  "BenchmarkP90",
  "Scale3",
] as const satisfies readonly (keyof typeof ChartColor)[];
const PIE_SERIES_COLOR_SLOTS = [
  "Scale1",
  "Scale2",
  "Scale3",
  "Scale4",
  "Scale5",
] as const satisfies readonly (keyof typeof ChartColor)[];
const BENCHMARK_COLOR_SLOTS = [
  "Benchmark1",
  "Benchmark2",
  "Benchmark3",
  "Benchmark4",
  "Benchmark5",
] as const satisfies readonly (keyof typeof ChartColor)[];
const COMPONENT_COLOR_SLOTS = [
  "Green",
  "Blue",
  "Orange1",
  "Orange2",
  "Red",
  "Scale3",
  "Scale4",
  "Orange3",
  "Orange4",
] as const satisfies readonly (keyof typeof ChartColor)[];
const PROJECT_COLOR_SLOTS = [
  "Blue",
  "Green",
  "Scale3",
  "GoalLine",
  "Benchmark1",
  "Red",
  "BenchmarkP90",
  "Orange1",
  "Benchmark3",
  "Orange2",
  "BenchmarkPreAI",
  "BenchmarkMedian",
  "BenchmarkP99",
  "Orange3",
  "Benchmark2",
  "Scale4",
] as const satisfies readonly (keyof typeof ChartColor)[];

// Literal and rendering views share slot identities, including intentional enum aliases.
function selectSlots<Slot extends keyof typeof ChartColor, Value>(
  values: Record<keyof typeof ChartColor, Value>,
  slots: readonly Slot[],
): readonly Value[] {
  return slots.map(name => values[name]);
}

export const SERIES_COLORS = selectSlots(ChartColor, SERIES_COLOR_SLOTS);
export const SERIES_COLOR_TOKENS = selectSlots(ChartColorToken, SERIES_COLOR_SLOTS);
export const PIE_SERIES_COLORS = selectSlots(ChartColor, PIE_SERIES_COLOR_SLOTS);
export const PIE_SERIES_COLOR_TOKENS = selectSlots(ChartColorToken, PIE_SERIES_COLOR_SLOTS);
export const BENCHMARK_COLORS = selectSlots(ChartColor, BENCHMARK_COLOR_SLOTS);
export const BENCHMARK_COLOR_TOKENS = selectSlots(ChartColorToken, BENCHMARK_COLOR_SLOTS);
export const COMPONENT_COLORS = selectSlots(ChartColor, COMPONENT_COLOR_SLOTS);
export const COMPONENT_COLOR_TOKENS = selectSlots(ChartColorToken, COMPONENT_COLOR_SLOTS);
export const PROJECT_COLORS = selectSlots(ChartColor, PROJECT_COLOR_SLOTS);
export const PROJECT_COLOR_TOKENS = selectSlots(ChartColorToken, PROJECT_COLOR_SLOTS);
export const ACCOUNT_SERIES_COLORS: readonly AccountChartColor[] = Object.values(AccountChartColor);
export const ACCOUNT_SERIES_COLOR_TOKENS: readonly AccountChartColorToken[] =
  Object.values(AccountChartColorToken);

function getColorDeclarations(colors: Record<string, string>, tokens: Record<string, string>) {
  return Object.fromEntries(
    Object.entries(tokens).map(([name, token]) => [token.slice(4, -1), colors[name]]),
  );
}

export const CHART_COLOR_DECLARATIONS: Record<string, string> = {
  ...getColorDeclarations(ChartColor, ChartColorToken),
  ...getColorDeclarations(AccountChartColor, AccountChartColorToken),
  ...getColorDeclarations(ProviderChartColor, ProviderChartColorToken),
  ...getColorDeclarations(AiCodeAssistantColor, AiCodeAssistantColorToken),
  ...getColorDeclarations(CustomAIAgentChartColor, CustomAIAgentChartColorToken),
  ...getColorDeclarations(RouterModelFamilyChartColor, RouterModelFamilyChartColorToken),
  ...getColorDeclarations(ChartContrastColor, ChartContrastColorToken),
  ...getColorDeclarations(RankingChartColor, RankingChartColorToken),
  ...getColorDeclarations(WeeklyInsightTierChartColor, WeeklyInsightTierChartColorToken),
  ...getColorDeclarations(PRTypeChartColor, PRTypeChartColorToken),
  ...getColorDeclarations(LOCTypeChartColor, LOCTypeChartColorToken),
};
