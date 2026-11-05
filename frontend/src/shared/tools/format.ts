import { TimeGranularity } from "./timeGranularity";

const INTEGER_FORMATTER = new Intl.NumberFormat(undefined, { maximumFractionDigits: 0 });

const BIG_INTEGER_FORMATTER = new Intl.NumberFormat(undefined, {
  compactDisplay: "short",
  maximumFractionDigits: 0,
  notation: "compact",
});

const BIG_DECIMAL_FORMATTER = new Intl.NumberFormat(undefined, {
  compactDisplay: "short",
  maximumFractionDigits: 1,
  notation: "compact",
});

const USD_FORMATTER = new Intl.NumberFormat(undefined, {
  currency: "USD",
  style: "currency",
});

const WHOLE_USD_FORMATTER = new Intl.NumberFormat(undefined, {
  currency: "USD",
  maximumFractionDigits: 0,
  style: "currency",
});

const COMPACT_USD_FORMATTER = new Intl.NumberFormat(undefined, {
  currency: "USD",
  maximumFractionDigits: 0,
  notation: "compact",
  style: "currency",
});

/**
 * Formats a byte count for human display (e.g. "512 B", "1.4 KB", "3.2 MB").
 * Uses 1024-based units. Suitable for short status strings — for precise
 * formatting (decimal vs binary, larger units), build a dedicated formatter.
 */
export function formatBytes(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`;
  return `${(bytes / (1024 * 1024)).toFixed(1)} MB`;
}

/**
 * Formats a number as a compact integer, handling large numbers (e.g. "2K", "1.5M").
 *
 * Single-argument intentionally — this is passed directly as a Recharts
 * `tickFormatter`, which is invoked as `(value, index)`. Adding a second
 * positional parameter would leak the tick index into the formatter and
 * produce inconsistent axis labels.
 */
export function formatBigInteger(value: number): string {
  if (isNaN(value)) return "-";

  return BIG_INTEGER_FORMATTER.format(value);
}

/**
 * Like `formatBigInteger` but keeps one decimal place (e.g. "2.2K", "1.5M").
 *
 * Prefer this over `formatBigInteger` for chart axis ticks: auto-generated
 * ticks often land on non-round values (e.g. 1500, 2250), and integer compact
 * rounding collapses those to the same label ("2K", "2K"). Keeping one decimal
 * keeps adjacent ticks distinct ("1.5K", "2.3K") while round values still read
 * cleanly ("2K", "473K") since trailing zeros are dropped.
 */
export function formatBigDecimal(value: number): string {
  if (isNaN(value)) return "-";

  return BIG_DECIMAL_FORMATTER.format(value);
}

export function formatNumber(value: number, maximumFractionDigits = 2): string {
  if (isNaN(value)) return "-";

  return value.toLocaleString(undefined, { maximumFractionDigits });
}

/** Formats a length of time in hours: minutes under an hour, days from two days. */
export function formatHours(hours: number): string {
  if (hours < 1) return `${Math.round(hours * 60)}m`;
  if (hours < 48) return `${hours.toFixed(hours < 10 ? 1 : 0)}h`;
  return `${(hours / 24).toFixed(1)}d`;
}

export function formatInteger(value: number): string {
  if (isNaN(value)) return "-";

  return INTEGER_FORMATTER.format(value);
}

/**
 * Suffixes for `formatMetricRateForGranularity` (matches "/d", "/w", etc.).
 */
const RATE_SUFFIX: Readonly<Record<TimeGranularity, string>> = {
  [TimeGranularity.Day]: "d",
  [TimeGranularity.Month]: "m",
  [TimeGranularity.Quarter]: "q",
  [TimeGranularity.TwoWeek]: "2w",
  [TimeGranularity.Week]: "w",
  [TimeGranularity.Year]: "y",
};

/**
 * Returns the short rate suffix for a granularity (e.g. "w", "2w", "m"), matching the
 * suffix used by `formatMetricRateForGranularity`. Use when a caller has already
 * formatted the value (via `formatCellData`, etc.) and just needs to append the unit.
 */
export function getMetricRateSuffix(granularity: TimeGranularity): string {
  return RATE_SUFFIX[granularity];
}

/**
 * Formats a period-total metric as value per chart granularity unit (e.g. "12.3/w" for weekly).
 * Matches team table SHOW_RATE columns and the child orgs output table.
 */
export function formatMetricRateForGranularity(
  value: number,
  rateDivisor: number,
  granularity: TimeGranularity,
): string {
  const safeDivisor = Math.max(rateDivisor, 1);
  return `${formatNumber(value / safeDivisor)}/${RATE_SUFFIX[granularity]}`;
}

/**
 * Formats a number as a percentage.
 *
 * @example
 * formatPercentage(0.5) // "50%"
 * formatPercentage(999999) // "∞%"
 */
export function formatPercentage(value: number): string {
  if (isNaN(value)) return "-";
  if (!isFinite(value) || value >= 999999) return "∞%";

  return `${formatInteger(value * 100)}%`;
}

/**
 * Formats a number as a USD amount.
 */
export function formatUSD(value: number): string {
  if (isNaN(value)) return "-";

  return USD_FORMATTER.format(value);
}

/** Formats a USD amount in whole dollars, e.g. "$1,180". */
export function formatWholeUSD(value: number): string {
  if (isNaN(value)) return "-";

  return WHOLE_USD_FORMATTER.format(value);
}

/**
 * Formats a number as a compact USD amount.
 * Shows values like "$1.2K" instead of "$1,234.56".
 * Useful for chart Y-axis labels where space is limited.
 */
export function formatUSDCompact(value: number): string {
  if (isNaN(value)) return "-";

  return COMPACT_USD_FORMATTER.format(value);
}

/**
 * Formats a value in its native currency without any FX conversion.
 * Used by the R&D capitalization view where each employee's salary is kept
 * in the currency reported by their HR system (USD, CAD, GBP, ...).
 *
 * Falls back to plain number formatting if `currency` isn't a valid ISO-4217 code.
 */
export function formatCurrencyNative(value: number, currency: string | null | undefined): string {
  if (isNaN(value)) return "-";
  if (currency == null || currency === "") {
    return formatNumber(value, 0);
  }
  try {
    return new Intl.NumberFormat(undefined, {
      currency,
      maximumFractionDigits: 0,
      style: "currency",
    }).format(value);
  } catch {
    return `${formatNumber(value, 0)} ${currency}`;
  }
}

const CURRENCY_STORAGE_KEY = "preferred-currency";

/**
 * Gets the user's preferred currency from browser locale or localStorage
 */
export function getUserCurrency(): string {
  if (typeof window === "undefined") return "USD";

  const stored = localStorage?.getItem(CURRENCY_STORAGE_KEY);
  if (stored != null && stored !== "") return stored;

  try {
    const locale = navigator.language !== "" ? navigator.language : "en-US";

    const localeToCurrency: Record<string, string> = {
      de: "EUR",
      "de-CH": "CHF",
      "de-DE": "EUR",
      "en-AU": "AUD",
      "en-CA": "CAD",
      "en-GB": "GBP",
      "en-US": "USD",
      es: "EUR",
      "es-AR": "USD",
      "es-CO": "USD",
      "es-ES": "EUR",
      fr: "EUR",
      "fr-CH": "CHF",
      "fr-FR": "EUR",
      hi: "INR",
      "hi-IN": "INR",
      it: "EUR",
      "it-IT": "EUR",
      ja: "JPY",
      "ja-JP": "JPY",
      ko: "KRW",
      "ko-KR": "KRW",
      zh: "CNY",
      "zh-CN": "CNY",
    };

    const matchedCurrency = localeToCurrency[locale];
    if (matchedCurrency != null && matchedCurrency !== "") {
      return matchedCurrency;
    }

    const language = locale.split("-")[0];
    const languageCurrency = localeToCurrency[language];
    if (languageCurrency != null && languageCurrency !== "") {
      return languageCurrency;
    }

    return "USD";
  } catch {
    return "USD";
  }
}

// Fallback exchange rates used when API rates are not available.
const FALLBACK_RATES: Record<string, number> = {
  AUD: 1.55,
  CAD: 1.36,
  CHF: 0.88,
  CNY: 7.24,
  EUR: 0.92,
  GBP: 0.79,
  INR: 84.0,
  JPY: 149.0,
  KRW: 1350.0,
  USD: 1.0,
};

// Dynamic exchange rates injected from the API.
let dynamicRates: Record<string, number> | null = null;

/**
 * Sets the exchange rates fetched from the API.
 * Called by useExchangeRates hook when rates are loaded.
 */
export function setExchangeRates(rates: Record<string, number>): void {
  dynamicRates = rates;
}

/**
 * Gets exchange rate from USD to target currency.
 * Uses dynamically fetched rates when available, falls back to hardcoded rates otherwise.
 */
export function getExchangeRate(targetCurrency: string): number {
  const rates = dynamicRates ?? FALLBACK_RATES;
  return rates[targetCurrency] ?? 1.0;
}

/**
 * Converts USD amount to user's local currency and formats it using native JavaScript
 */
const BREAKDOWN_WHOLE_UNITS_FROM = 10000;

/** `wholeUnitsFrom` drops cents from that amount up; `null` always keeps them. */
function formatLocaleCurrencyValue(usdValue: number, wholeUnitsFrom: number | null) {
  if (isNaN(usdValue)) return "-";

  const fallbackFormatter =
    wholeUnitsFrom != null && Math.abs(usdValue) >= wholeUnitsFrom
      ? WHOLE_USD_FORMATTER
      : USD_FORMATTER;
  if (typeof window === "undefined") {
    return fallbackFormatter.format(usdValue);
  }

  const userCurrency = getUserCurrency();
  const exchangeRate = getExchangeRate(userCurrency);

  try {
    const convertedAmount = usdValue * exchangeRate;
    const locale = navigator.language !== "" ? navigator.language : "en-US";
    const formatter = new Intl.NumberFormat(locale, {
      currency: userCurrency,
      ...(wholeUnitsFrom != null && Math.abs(convertedAmount) >= wholeUnitsFrom
        ? { maximumFractionDigits: 0 }
        : {}),
      style: "currency",
    });
    return formatter.format(convertedAmount);
  } catch {
    return fallbackFormatter.format(usdValue);
  }
}

export function formatLocaleCurrency(usdValue: number): string {
  return formatLocaleCurrencyValue(usdValue, null);
}

/** Shows cents below $10,000 and whole currency units from $10,000 onward. */
export function formatLocaleCurrencyBreakdown(usdValue: number): string {
  return formatLocaleCurrencyValue(usdValue, BREAKDOWN_WHOLE_UNITS_FROM);
}

/** Shows cents on small spend totals and whole currency units from 1,000 onward. */
export function formatLocaleCurrencyHeadline(usdValue: number): string {
  return formatLocaleCurrencyValue(usdValue, 1000);
}

// Bedrock / Vertex AI provider prefixes to strip before formatting
// e.g. "us.anthropic.claude-3-5-sonnet-v1:0" -> "claude-3-5-sonnet-v1:0"
const MODEL_PROVIDER_PREFIX =
  /^(?:[a-z]+\.)*(?:anthropic|openai|google)\.(?=claude|gpt|gemini|o\d)/i;

// Trailing date snapshot in model ids, e.g. "claude-3-5-sonnet-20241022"
const MODEL_DATE_SUFFIX = /-\d{8}$/;

// Model family acronyms that should stay fully uppercased in display names
// e.g. "gpt-5.5" -> "GPT 5.5" (not "Gpt 5.5")
const MODEL_NAME_ACRONYMS = new Set(["glm", "gpt"]);

/**
 * Canonicalizes a raw model id for grouping/deduplication: strips provider
 * prefixes and trailing date snapshots, and lowercases the result so ids like
 * "claude-opus-4-5" and "us.anthropic.claude-opus-4-5-20250930" map to the
 * same canonical id.
 */
export function canonicalizeModelId(modelId: string): string {
  return modelId.replace(MODEL_PROVIDER_PREFIX, "").replace(MODEL_DATE_SUFFIX, "").toLowerCase();
}

export const ROUTER_MODEL_DISPLAY_NAMES = {
  "anthropic/claude-fable-5": "Claude Fable 5",
  "anthropic/claude-fable-5.1": "Claude Fable 5.1",
  "anthropic/claude-haiku-4.5": "Claude Haiku 4.5",
  "anthropic/claude-haiku-5.5": "Claude Haiku 5.5",
  "anthropic/claude-opus-4-5": "Claude Opus 4.5",
  "anthropic/claude-opus-4-6": "Claude Opus 4.6",
  "anthropic/claude-opus-4-7": "Claude Opus 4.7",
  "anthropic/claude-opus-4.8": "Claude Opus 4.8",
  "anthropic/claude-opus-5": "Claude Opus 5",
  "anthropic/claude-opus-5.5": "Claude Opus 5.5",
  "anthropic/claude-sonnet-4-5": "Claude Sonnet 4.5",
  "anthropic/claude-sonnet-4.5": "Claude Sonnet 4.5",
  "anthropic/claude-sonnet-4.6": "Claude Sonnet 4.6",
  "anthropic/claude-sonnet-5": "Claude Sonnet 5",
  "anthropic/claude-sonnet-5.5": "Claude Sonnet 5.5",
  "cursor/composer": "Composer",
  "cursor/composer-1": "Composer 1",
  "cursor/composer-1.5": "Composer 1.5",
  "cursor/composer-2": "Composer 2",
  "cursor/composer-2.5": "Composer 2.5",
  "cursor/composer-2.5-fast": "Composer 2.5 Fast",
  "deepseek/deepseek-v3.1": "DeepSeek V3.1",
  "deepseek/deepseek-v4-flash": "DeepSeek V4 Flash",
  "deepseek/deepseek-v4-pro": "DeepSeek V4 Pro",
  "deepseek/deepseek-v4-pro-0813": "DeepSeek V4 Pro 0813",
  "deepseek/deepseek-v4.1-flash": "DeepSeek V4.1 Flash",
  "google/gemini-2.5-flash": "Gemini 2.5 Flash",
  "google/gemini-2.5-pro": "Gemini 2.5 Pro",
  "google/gemini-3-flash": "Gemini 3 Flash",
  "google/gemini-3-pro": "Gemini 3 Pro",
  "google/gemini-3.1-flash-lite-preview": "Gemini 3.1 Flash Lite Preview",
  "google/gemini-3.1-pro-preview": "Gemini 3.1 Pro Preview",
  "google/gemini-3.5-flash": "Gemini 3.5 Flash",
  "google/gemini-3.7-flash": "Gemini 3.7 Flash",
  "google/gemini-3.8-flash": "Gemini 3.8 Flash",
  "inclusionai/ling-3.0-flash": "Ling 3.0 Flash",
  "minimax/minimax-m2.7": "MiniMax M2.7",
  "minimax/minimax-m3": "MiniMax M3",
  "minimax/minimax-m3.1-flash": "MiniMax M3.1 Flash",
  "moonshotai/kimi-k2-thinking": "Kimi K2 Thinking",
  "moonshotai/kimi-k2.5": "Kimi K2.5",
  "moonshotai/kimi-k2.6": "Kimi K2.6",
  "moonshotai/kimi-k2.7": "Kimi K2.7",
  "moonshotai/kimi-k2.7-code": "Kimi K2.7 Code",
  "moonshotai/kimi-k3": "Kimi K3",
  "openai/gpt-4o-mini": "GPT-4o Mini",
  "openai/gpt-5": "GPT-5",
  "openai/gpt-5-mini": "GPT-5 Mini",
  "openai/gpt-5.1": "GPT-5.1",
  "openai/gpt-5.1-codex": "GPT-5.1 Codex",
  "openai/gpt-5.1-codex-max": "GPT-5.1 Codex Max",
  "openai/gpt-5.1-codex-mini": "GPT-5.1 Codex Mini",
  "openai/gpt-5.2": "GPT-5.2",
  "openai/gpt-5.2-codex": "GPT-5.2 Codex",
  "openai/gpt-5.3-codex": "GPT-5.3 Codex",
  "openai/gpt-5.3-codex-spark": "GPT-5.3 Codex Spark",
  "openai/gpt-5.4": "GPT-5.4",
  "openai/gpt-5.4-mini": "GPT-5.4 Mini",
  "openai/gpt-5.4-nano": "GPT-5.4 Nano",
  "openai/gpt-5.5": "GPT-5.5",
  "openai/gpt-5.5-pro": "GPT-5.5 Pro",
  "openai/gpt-5.6-luna": "GPT-5.6 Luna",
  "openai/gpt-5.6-sol": "GPT-5.6 Sol",
  "openai/gpt-5.6-terra": "GPT-5.6 Terra",
  "openai/gpt-6-astra": "GPT-6 Astra",
  "openai/gpt-6-luna": "GPT-6 Luna",
  "openai/gpt-6-sol": "GPT-6 Sol",
  "openai/gpt-6.1-sol": "GPT-6.1 Sol",
  "openai/gpt-6.1-terra": "GPT-6.1 Terra",
  "openai/o3": "O3",
  "openai/o4-mini": "O4 Mini",
  "qwen/qwen3-coder-30b": "Qwen3 Coder 30B",
  "qwen/qwen3-coder-next": "Qwen3 Coder Next",
  "qwen/qwen3-next-80b-a3b-instruct": "Qwen3 Next 80B A3B Instruct",
  "qwen/qwen3.5-4b": "Qwen3.5 4B",
  "qwen/qwen3.6-27b": "Qwen3.6 27B",
  "qwen/qwen3.6-35b-a3b": "Qwen3.6 35B A3B",
  "qwen/qwen3.7-plus": "Qwen3.7 Plus",
  "qwen/qwen3.8-max": "Qwen3.8 Max",
  "x-ai/grok-4.3": "Grok 4.3",
  "x-ai/grok-4.5": "Grok 4.5",
  "x-ai/grok-4.5-beta": "Grok 4.5 Beta",
  "x-ai/grok-4.5-fast": "Grok 4.5 Fast",
  "x-ai/grok-4.6": "Grok 4.6",
  "x-ai/grok-4.6-fast": "Grok 4.6 Fast",
  "x-ai/grok-4.7": "Grok 4.7",
  "x-ai/grok-4.7-fast": "Grok 4.7 Fast",
  "xiaomi/mimo-v2.5": "MiMo V2.5",
  "xiaomi/mimo-v2.5-pro": "MiMo V2.5 Pro",
  "xiaomi/mimo-v2.6-flash": "MiMo V2.6 Flash",
  "xiaomi/mimo-v2.6-pro": "MiMo V2.6 Pro",
  "z-ai/glm-5": "GLM-5",
  "z-ai/glm-5.1": "GLM-5.1",
  "z-ai/glm-5.2": "GLM-5.2",
  "z-ai/glm-5.3": "GLM-5.3",
  "z-ai/glm-5.3-flash": "GLM-5.3 Flash",
} as const;

type RouterModelDisplayName =
  (typeof ROUTER_MODEL_DISPLAY_NAMES)[keyof typeof ROUTER_MODEL_DISPLAY_NAMES];

// GPT short labels read as the model line, then its version ("Luna 6"), like "Opus 5.5".
export const ROUTER_MODEL_SHORT_NAMES = {
  "Claude Fable 5": "Fable 5",
  "Claude Fable 5.1": "Fable 5.1",
  "Claude Haiku 4.5": "Haiku 4.5",
  "Claude Haiku 5.5": "Haiku 5.5",
  "Claude Opus 4.5": "Opus 4.5",
  "Claude Opus 4.6": "Opus 4.6",
  "Claude Opus 4.7": "Opus 4.7",
  "Claude Opus 4.8": "Opus 4.8",
  "Claude Opus 5": "Opus 5",
  "Claude Opus 5.5": "Opus 5.5",
  "Claude Sonnet 4.5": "Sonnet 4.5",
  "Claude Sonnet 4.6": "Sonnet 4.6",
  "Claude Sonnet 5": "Sonnet 5",
  "Claude Sonnet 5.5": "Sonnet 5.5",
  "DeepSeek V4.1 Flash": "DeepSeek 4.1 Flash",
  "DeepSeek V4 Flash": "DeepSeek 4 Flash",
  "DeepSeek V4 Pro": "DeepSeek 4 Pro",
  "Gemini 3.1 Flash Lite Preview": "Gemini 3.1 Flash Lite Preview",
  "Gemini 3.1 Pro Preview": "Gemini 3.1 Pro Preview",
  "Gemini 3.8 Flash": "Gemini 3.8 Flash",
  "GPT-4o Mini": "4o Mini",
  "GPT-5.6 Luna": "Luna 5.6",
  "GPT-5.6 Sol": "Sol 5.6",
  "GPT-5.6 Terra": "Terra 5.6",
  "GPT-6.1 Sol": "Sol 6.1",
  "GPT-6.1 Terra": "Terra 6.1",
  "GPT-6 Astra": "Astra 6",
  "GPT-6 Luna": "Luna 6",
  "GPT-6 Sol": "Sol 6",
  "Grok 4.6": "Grok 4.6",
  "Grok 4.7": "Grok 4.7",
  "MiniMax M3": "M3",
  "MiniMax M3.1 Flash": "M3.1 Flash",
} satisfies Partial<Record<RouterModelDisplayName, string>>;

// Router slugs carry reasoning effort as a ":effort" suffix ("openai/gpt-6.1-sol:xhigh").
const ROUTER_MODEL_EFFORT_LABELS = {
  high: "high",
  low: "low",
  medium: "medium",
  xhigh: "extra high",
} as const;

type RouterModelEffort = keyof typeof ROUTER_MODEL_EFFORT_LABELS;

function isRouterModelEffort(value: string): value is RouterModelEffort {
  return Object.prototype.hasOwnProperty.call(ROUTER_MODEL_EFFORT_LABELS, value);
}

const ROUTER_MODEL_EFFORT_DISPLAY_SUFFIX = new RegExp(
  ` \\((?:${Object.values(ROUTER_MODEL_EFFORT_LABELS).join("|")})\\)$`,
);

/** Short names label the model line only, so an effort variant shares its base model's short name. */
export function formatRouterModelShortName(modelName: string): string {
  const baseModelName = modelName.replace(ROUTER_MODEL_EFFORT_DISPLAY_SUFFIX, "");
  return (
    ROUTER_MODEL_SHORT_NAMES[baseModelName as keyof typeof ROUTER_MODEL_SHORT_NAMES] ??
    baseModelName
  );
}

export function formatRouterModelName(modelSlug: string): string {
  if (Object.prototype.hasOwnProperty.call(ROUTER_MODEL_DISPLAY_NAMES, modelSlug)) {
    return ROUTER_MODEL_DISPLAY_NAMES[modelSlug as keyof typeof ROUTER_MODEL_DISPLAY_NAMES];
  }

  const effortSeparatorIndex = modelSlug.lastIndexOf(":");
  const effort = modelSlug.slice(effortSeparatorIndex + 1);
  if (effortSeparatorIndex !== -1 && isRouterModelEffort(effort)) {
    const baseSlug = modelSlug.slice(0, effortSeparatorIndex);
    const baseName = formatRouterModelName(baseSlug);
    if (baseName !== baseSlug) return `${baseName} (${ROUTER_MODEL_EFFORT_LABELS[effort]})`;
  }

  const provider = modelSlug.includes("/")
    ? modelSlug.slice(0, modelSlug.lastIndexOf("/"))
    : undefined;
  const unqualifiedModelSlug = modelSlug
    .slice(modelSlug.lastIndexOf("/") + 1)
    .replace(/(?:-1m|\[1m\])$/, "");
  const dottedVersionSlug = unqualifiedModelSlug.replace(/-(\d+)-(\d+)$/, "-$1.$2");
  const alias = Object.entries(ROUTER_MODEL_DISPLAY_NAMES).find(([canonicalSlug]) => {
    const modelNameStart = canonicalSlug.lastIndexOf("/") + 1;
    return (
      (provider == null || canonicalSlug.slice(0, modelNameStart - 1) === provider) &&
      (canonicalSlug.slice(modelNameStart) === unqualifiedModelSlug ||
        canonicalSlug.slice(modelNameStart) === dottedVersionSlug)
    );
  });
  if (alias != null) return alias[1];

  return modelSlug;
}

/**
 * Formats a model ID into a display name.
 * e.g., "claude-3-5-sonnet-20241022" -> "Claude 3.5 Sonnet"
 * e.g., "us.anthropic.claude-3-5-sonnet-v1:0" -> "Claude 3.5 Sonnet V1:0"
 */
export function formatModelName(modelId: string): string {
  if (modelId == null || modelId === "") {
    return "Unknown Model";
  }

  const routerDisplayName = formatRouterModelName(modelId);
  if (routerDisplayName !== modelId) return routerDisplayName;

  if (modelId.includes("/")) return modelId;

  const stripped = modelId.replace(MODEL_PROVIDER_PREFIX, "");
  const withoutDate = stripped.replace(MODEL_DATE_SUFFIX, "");

  return withoutDate
    .split("-")
    .map(part => {
      if (/^\d+$/.test(part)) {
        return part;
      }
      if (MODEL_NAME_ACRONYMS.has(part.toLowerCase())) {
        return part.toUpperCase();
      }
      return part.charAt(0).toUpperCase() + part.slice(1);
    })
    .join(" ")
    .replace(/(\d+) (\d+)/g, "$1.$2");
}
