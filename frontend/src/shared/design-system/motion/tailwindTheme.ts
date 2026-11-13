import motionTokens from "./motion.json";

/** Tailwind `duration-*` classes backed by the `--wv-duration-*` tokens, e.g. `duration-ui`. */
export const MOTION_DURATION_THEME = Object.fromEntries(
  Object.keys(motionTokens.durations).map(name => [name, `var(--wv-duration-${name})`]),
);

/** Tailwind `ease-*` classes backed by the `--wv-ease-*` curve tokens, e.g. `ease-out-cubic`. */
export const MOTION_EASING_THEME = Object.fromEntries(
  motionTokens.curves.map(curve => [
    curve.cssVariable.replace("--wv-ease-", ""),
    `var(${curve.cssVariable})`,
  ]),
);
