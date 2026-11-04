import { RouterModelFamilyChartColor, RouterModelFamilyChartColorToken } from "@/shared/tools/chartColors";
import { formatRouterModelName, ROUTER_MODEL_DISPLAY_NAMES } from "@/shared/tools/format";

/** Who makes a model, which can differ from the provider that served it (e.g. a gateway). */
export enum RouterModelFamily {
  Anthropic = "anthropic",
  Google = "google",
  OpenAI = "openai",
  OpenSource = "open_source",
  Unknown = "unknown",
  XAI = "xai",
}

const FAMILY_COLOR_SLOTS = {
  [RouterModelFamily.Anthropic]: [
    "Anthropic1",
    "Anthropic2",
    "Anthropic3",
    "Anthropic4",
    "Anthropic5",
  ],
  [RouterModelFamily.Google]: ["Google1", "Google2", "Google3", "Google4", "Google5"],
  [RouterModelFamily.OpenAI]: ["OpenAI1", "OpenAI2", "OpenAI3", "OpenAI4", "OpenAI5"],
  [RouterModelFamily.OpenSource]: [
    "OpenSource1",
    "OpenSource2",
    "OpenSource3",
    "OpenSource4",
    "OpenSource5",
  ],
  [RouterModelFamily.Unknown]: ["Unknown1", "Unknown2", "Unknown3", "Unknown4", "Unknown5"],
  [RouterModelFamily.XAI]: ["XAI1", "XAI2", "XAI3", "XAI4", "XAI5"],
} as const satisfies Readonly<
  Record<RouterModelFamily, readonly (keyof typeof RouterModelFamilyChartColor)[]>
>;

const FAMILY_SHADES = Object.fromEntries<readonly string[]>(
  Object.entries(FAMILY_COLOR_SLOTS).map(([family, slots]) => [
    family,
    slots.map(slot => RouterModelFamilyChartColor[slot]),
  ]),
) as Readonly<Record<RouterModelFamily, readonly string[]>>;
const FAMILY_SHADE_TOKENS = Object.fromEntries<readonly string[]>(
  Object.entries(FAMILY_COLOR_SLOTS).map(([family, slots]) => [
    family,
    slots.map(slot => RouterModelFamilyChartColorToken[slot]),
  ]),
) as Readonly<Record<RouterModelFamily, readonly string[]>>;

// Checked in order against the model name without any `org/` prefix or `:effort` suffix, so
// gateway slugs like `anthropic/claude-opus-5` land in the maker's family.
const FAMILY_PATTERNS: readonly (readonly [RegExp, RouterModelFamily])[] = [
  // Open-weight lines only; a maker's hosted-only models (e.g. Qwen Max) stay unknown.
  [
    /^(gpt-oss|deepseek|glm|kimi|minimax|mimo|ling|llama|mistral|gemma)/,
    RouterModelFamily.OpenSource,
  ],
  [/^qwen(?!.*max)/, RouterModelFamily.OpenSource],
  [/^claude-/, RouterModelFamily.Anthropic],
  [/^(gpt-|codex|o\d)/, RouterModelFamily.OpenAI],
  [/^gemini-/, RouterModelFamily.Google],
  [/^grok-/, RouterModelFamily.XAI],
];

export const MODEL_FAMILY_LABEL: Readonly<Record<RouterModelFamily, string>> = {
  [RouterModelFamily.Anthropic]: "Anthropic",
  [RouterModelFamily.Google]: "Google",
  [RouterModelFamily.OpenAI]: "OpenAI",
  [RouterModelFamily.OpenSource]: "Open-weight",
  [RouterModelFamily.Unknown]: "Other",
  [RouterModelFamily.XAI]: "xAI",
};

// The second shade, so a family's single series reads as that family without being its darkest.
export const MODEL_FAMILY_COLOR = Object.fromEntries(
  Object.values(RouterModelFamily).map(family => [family, FAMILY_SHADES[family][1]]),
) as Readonly<Record<RouterModelFamily, string>>;
export const MODEL_FAMILY_COLOR_TOKENS = Object.fromEntries(
  Object.values(RouterModelFamily).map(family => [family, FAMILY_SHADE_TOKENS[family][1]]),
) as Readonly<Record<RouterModelFamily, string>>;

export function modelFamily(model: string): RouterModelFamily {
  const name = model.toLowerCase();
  const baseName = name.slice(name.lastIndexOf("/") + 1).split(":")[0];
  const match = FAMILY_PATTERNS.find(([pattern]) => pattern.test(baseName));
  return match == null ? RouterModelFamily.Unknown : match[1];
}

// Each family's catalog models in catalog order, which follows releases, so a family's current
// models sit next to each other and take different shades. Keyed by display name so slug variants
// of one model (with or without an `org/` prefix, dashed or dotted versions) share a shade.
const CATALOG_POSITION_BY_DISPLAY_NAME = (() => {
  const familyCounts = new Map<RouterModelFamily, number>();
  const positions = new Map<string, number>();
  for (const [slug, displayName] of Object.entries(ROUTER_MODEL_DISPLAY_NAMES)) {
    const family = modelFamily(slug);
    const position = familyCounts.get(family) ?? 0;
    familyCounts.set(family, position + 1);
    positions.set(displayName, position);
  }
  return positions;
})();

/** A model's shade before collisions: its catalog position, or a hash for a model not listed. */
function baseShade(model: string) {
  const displayName = formatRouterModelName(model);
  const position = CATALOG_POSITION_BY_DISPLAY_NAME.get(displayName);
  if (position != null) return position;
  let hash = 0;
  for (const char of displayName) hash = (hash * 31 + char.charCodeAt(0)) >>> 0;
  return hash;
}

/**
 * Colors models by family, with a different shade per model. A model keeps the same shade in every
 * chart and row unless it collides with another model of its family in the same set.
 */
export function modelColors(models: readonly string[]): string[] {
  return colorsForModels(models, FAMILY_SHADES);
}

export function modelColorTokens(models: readonly string[]): string[] {
  return colorsForModels(models, FAMILY_SHADE_TOKENS);
}

function colorsForModels(
  models: readonly string[],
  shadesByFamily: Readonly<Record<RouterModelFamily, readonly string[]>>,
): string[] {
  const usedShades = new Map<RouterModelFamily, Set<number>>();
  // Slug variants of one model (e.g. dashed and dotted versions) are one model, not a collision.
  const colorByDisplayName = new Map<string, string>();
  return models.map(model => {
    const displayName = formatRouterModelName(model);
    const sharedColor = colorByDisplayName.get(displayName);
    if (sharedColor != null) return sharedColor;
    const family = modelFamily(model);
    const shades = shadesByFamily[family];
    const used = usedShades.get(family) ?? new Set<number>();
    usedShades.set(family, used);
    let shade = baseShade(model) % shades.length;
    while (used.has(shade) && used.size < shades.length) shade = (shade + 1) % shades.length;
    used.add(shade);
    colorByDisplayName.set(displayName, shades[shade]);
    return shades[shade];
  });
}
