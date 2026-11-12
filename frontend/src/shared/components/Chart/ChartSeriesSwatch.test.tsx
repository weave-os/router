import { render } from "@testing-library/react";

import { ChartSeriesSwatch, isDottedSeries, isHatchedBarSeries } from "./ChartSeriesSwatch";
import { ChartFillPattern, ChartSeriesType } from "./types";

const HATCH_CLASS_FRAGMENT = "repeating-linear-gradient";

test("dashed bar series are hatched, including bars by default type", () => {
  expect(isHatchedBarSeries({ dashed: true })).toBe(true);
  expect(isHatchedBarSeries({ dashed: true, type: ChartSeriesType.Bar })).toBe(true);
});

test("solid series and dashed lines or areas are not hatched", () => {
  expect(isHatchedBarSeries(undefined)).toBe(false);
  expect(isHatchedBarSeries({})).toBe(false);
  expect(isHatchedBarSeries({ dashed: false, type: ChartSeriesType.Bar })).toBe(false);
  expect(isHatchedBarSeries({ dashed: true, type: ChartSeriesType.Line })).toBe(false);
  expect(isHatchedBarSeries({ dashed: true, type: ChartSeriesType.Area })).toBe(false);
});

test("a hatched swatch stripes the series color instead of filling it solid", () => {
  const { container } = render(<ChartSeriesSwatch color="var(--series)" hatched />);
  const swatch = container.firstElementChild as HTMLElement;

  expect(swatch.className).toContain(HATCH_CLASS_FRAGMENT);
  expect(swatch.className).not.toContain("bg-[--color-bg]");
  expect(swatch.style.getPropertyValue("--color-bg")).toBe("var(--series)");
});

test("a solid swatch fills the series color", () => {
  const { container } = render(<ChartSeriesSwatch color="var(--series)" hatched={false} />);
  const swatch = container.firstElementChild as HTMLElement;

  expect(swatch.className).toContain("bg-[--color-bg]");
  expect(swatch.className).not.toContain(HATCH_CLASS_FRAGMENT);
});

test("a dotted area's swatch is dotted to match its fill", () => {
  expect(isDottedSeries({ fillPattern: ChartFillPattern.Dots })).toBe(true);
  expect(isDottedSeries({})).toBe(false);

  const { container } = render(<ChartSeriesSwatch color="var(--series)" dotted hatched={false} />);
  const swatch = container.firstElementChild as HTMLElement;

  expect(swatch.className).toContain("radial-gradient");
  expect(swatch.className).not.toContain("bg-[--color-bg]");
});
