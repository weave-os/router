import type { Config } from "tailwindcss";

import { routerUiTailwindPreset } from "./src/shared/theme/tailwindPreset";

const config: Config = {
  content: ["./src/**/*.{ts,tsx}"],
  presets: [routerUiTailwindPreset],
};

export default config;
