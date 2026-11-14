import { type ClassValue, clsx } from "clsx";

import { extendTailwindMerge } from "tailwind-merge";

import { TYPOGRAPHY_ROLE_TOKENS } from "./typography";

const mergeTypographyClasses = extendTailwindMerge({
  extend: {
    classGroups: {
      "font-weight": [{ font: ["ui", "navigation"] }],
      "font-size": [
        {
          text: ["2xs", ...Object.keys(TYPOGRAPHY_ROLE_TOKENS)],
        },
      ],
    },
  },
});

/**
 * Merges class names while treating semantic typography classes like Tailwind font-size classes.
 */
export function cn(...inputs: ClassValue[]) {
  return mergeTypographyClasses(clsx(inputs));
}
