import { ChartColor } from "@/shared/tools/chartColors";

import chroma from "chroma-js";

/**
 * Single-hue sequential ramp (light -> dark, weave orange by default) for encoding a magnitude —
 * e.g. a bubble chart's size value — as color intensity. Kept to one hue: varying hue across
 * bubbles would miscode identity onto values (like bin membership) that have none, so intensity
 * is the only channel that moves.
 *
 * Endpoints keep the base color's hue and change only its lightness, so the ramp stays a true
 * single-hue sequential scale rather than two eyeballed, unrelated hexes.
 */
function sequentialScale(baseColor: string) {
  return chroma
    .scale([chroma(baseColor).set("hsl.l", 0.82).hex(), chroma(baseColor).set("hsl.l", 0.34).hex()])
    .mode("lab");
}

const scalesByBaseColor = new Map<string, chroma.Scale>();

/**
 * Maps a normalized magnitude (0 = smallest, 1 = largest) to a color on the sequential ramp.
 * `fraction` is clamped to [0, 1] so out-of-range inputs degrade to an endpoint instead of
 * extrapolating outside the ramp. `baseColor` picks the ramp's hue; only lightness varies.
 */
export function getSequentialIntensityColor(
  fraction: number,
  baseColor: string = ChartColor.Scale1,
): string {
  const scale = scalesByBaseColor.get(baseColor) ?? sequentialScale(baseColor);
  scalesByBaseColor.set(baseColor, scale);
  return scale(Math.min(1, Math.max(0, fraction))).hex();
}
