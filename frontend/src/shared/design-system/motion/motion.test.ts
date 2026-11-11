import { readFileSync } from "node:fs";
import path from "node:path";

import { cubicBezier, MotionCurve } from "./motion";
import motionTokens from "./motion.json";
import { MOTION_DURATION_THEME, MOTION_EASING_THEME } from "./tailwindTheme";

const motionCss = readFileSync(path.join(import.meta.dirname, "motion.css"), "utf8");

const cssTokens = new Map(
  [...motionCss.matchAll(/(--wv-[\w-]+):\s*([^;]+);/g)].map(([, name, value]) => [name, value]),
);

test("CSS durations match motion.json", () => {
  for (const [name, durationMs] of Object.entries(motionTokens.durations)) {
    expect(cssTokens.get(`--wv-duration-${name}`)).toBe(`${durationMs}ms`);
  }
  expect(cssTokens.get("--wv-duration-micro-min")).toBe(
    `${motionTokens.microanimations.minimumDuration}ms`,
  );
  expect(cssTokens.get("--wv-micro-stagger-step")).toBe(
    `${motionTokens.microanimations.staggerStep}ms`,
  );
  expect(cssTokens.get("--wv-ease-linear")).toBe(motionTokens.utilities.linear);
});

test("CSS curves match motion.json", () => {
  for (const curve of motionTokens.curves) {
    expect(cssTokens.get(curve.cssVariable)).toBe(`cubic-bezier(${curve.points.join(", ")})`);
  }
});

test("MotionCurve covers exactly the motion.json curves", () => {
  expect(motionTokens.curves.map(curve => curve.id).sort()).toEqual(
    Object.values(MotionCurve).sort(),
  );
  for (const curve of motionTokens.curves) {
    expect(curve.points).toHaveLength(4);
  }
});

test("cubicBezier formats the curve without spaces", () => {
  expect(cubicBezier(MotionCurve.EaseInOutCubic)).toBe("cubic-bezier(0.65,0,0.35,1)");
});

test("Tailwind motion classes read the CSS tokens", () => {
  expect(Object.keys(MOTION_DURATION_THEME)).toEqual(Object.keys(motionTokens.durations));
  expect(Object.keys(MOTION_EASING_THEME)).toHaveLength(motionTokens.curves.length);
  for (const value of [
    ...Object.values(MOTION_DURATION_THEME),
    ...Object.values(MOTION_EASING_THEME),
  ]) {
    expect(cssTokens.has(value.replace(/^var\((.+)\)$/, "$1"))).toBe(true);
  }
});
