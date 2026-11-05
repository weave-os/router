import motionTokens from "./motion.json";

export enum MotionCurve {
  EaseOutExpo = "easeOutExpo",
  EaseOutQuart = "easeOutQuart",
  EaseOutCubic = "easeOutCubic",
  EaseInOutCubic = "easeInOutCubic",
  EaseInOutQuart = "easeInOutQuart",
}

/** For scripted animation (Web Animations API, Recharts) that cannot read the CSS tokens. */
export const MOTION_DURATION_MS = motionTokens.durations;

/** Delay between the starts of staggered elements; it never shortens an element's own duration. */
export const MOTION_STAGGER_STEP_MS = motionTokens.microanimations.staggerStep;

type CubicBezierPoints = [number, number, number, number];

// motion.test.ts asserts every MotionCurve has exactly one four-point entry in motion.json.
const CURVE_POINTS = Object.fromEntries(
  motionTokens.curves.map(curve => [curve.id, curve.points]),
) as Record<MotionCurve, CubicBezierPoints>;

/**
 * The curve as a `cubic-bezier()` timing function. The space-free form is valid CSS and also the
 * form Recharts' `animationEasing` parser accepts.
 */
export function cubicBezier(curve: MotionCurve) {
  const [x1, y1, x2, y2] = CURVE_POINTS[curve];
  return `cubic-bezier(${x1},${y1},${x2},${y2})` as const;
}
