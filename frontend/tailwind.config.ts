import type { Config } from "tailwindcss";

import { weaveUiTailwindPreset } from "@weave-os/ui/theme/tailwindPreset";

const config: Config = {
  content: ["./src/**/*.{ts,tsx}", "./node_modules/@weave-os/ui/**/*.js"],
  presets: [weaveUiTailwindPreset],
};

export default config;
