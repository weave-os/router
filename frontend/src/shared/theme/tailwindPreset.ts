import type { Config } from "tailwindcss";

import containerQueries from "@tailwindcss/container-queries";

import tailwindcssAnimate from "tailwindcss-animate";
import plugin from "tailwindcss/plugin";

import { MOTION_DURATION_THEME, MOTION_EASING_THEME } from "../design-system/motion/tailwindTheme";
import { CHART_COLOR_DECLARATIONS } from "../tools/chartColors";
import {
  TYPOGRAPHY_CSS_VARIABLES,
  TYPOGRAPHY_FONT_SIZE_THEME,
  TypographyRole,
} from "../tools/typography";

/**
 * Weave Router dashboard theme. Hosts add it to `presets` and list the shared sources in `content`.
 */
export const routerUiTailwindPreset = {
  darkMode: "class",
  safelist: Object.values(TypographyRole).map(role => `text-${role}`),
  plugins: [
    tailwindcssAnimate,
    containerQueries,
    plugin(api => {
      api.addBase({
        ":root, .dark": {
          ...CHART_COLOR_DECLARATIONS,
          ...TYPOGRAPHY_CSS_VARIABLES,
        },
      });
    }),
  ],
  theme: {
    colors: {
      accent: {
        DEFAULT: "hsl(var(--accent))",
        foreground: "hsl(var(--accent-foreground))",
      },
      background: "hsl(var(--background))",
      border: "var(--wv-legacy-border, hsl(var(--border)))",
      "border-darker": "var(--wv-legacy-border-darker, hsl(var(--border-darker)))",
      brand: "hsl(var(--brand))",
      card: {
        DEFAULT: "hsl(var(--card))",
        foreground: "hsl(var(--card-foreground))",
      },
      danger: {
        DEFAULT: "hsl(var(--danger))",
        foreground: "hsl(var(--danger-foreground))",
      },
      "filter-pill": {
        border: "hsl(var(--filter-pill-border))",
        DEFAULT: "hsl(var(--filter-pill))",
        foreground: "hsl(var(--filter-pill-foreground))",
      },
      foreground: "hsl(var(--foreground))",
      info: {
        DEFAULT: "hsl(var(--info))",
        foreground: "hsl(var(--info-foreground))",
      },
      input: "var(--wv-legacy-input, hsl(var(--input)))",
      muted: {
        DEFAULT: "hsl(var(--muted))",
        foreground: "hsl(var(--muted-foreground))",
      },
      popover: {
        DEFAULT: "hsl(var(--popover))",
        foreground: "hsl(var(--popover-foreground))",
      },
      primary: {
        DEFAULT: "hsl(var(--primary))",
        foreground: "hsl(var(--primary-foreground))",
      },
      ring: "hsl(var(--ring))",
      shortcuts: "hsl(var(--shortcuts))",
      "shortcuts-foreground": "hsl(var(--shortcuts-foreground))",
      "sidebar-background": "hsl(var(--sidebar-background))",
      "sidebar-section-background": "hsl(var(--sidebar-section-background))",
      success: {
        DEFAULT: "hsl(var(--success))",
        foreground: "hsl(var(--success-foreground))",
      },
      tooltip: {
        DEFAULT: "hsl(var(--tooltip))",
        foreground: "hsl(var(--tooltip-foreground))",
      },
      transparent: "transparent",
      warning: {
        DEFAULT: "hsl(var(--warning))",
        foreground: "hsl(var(--warning-foreground))",
      },
    },
    container: {
      center: true,
      padding: "2rem",
    },

    extend: {
      animation: {
        "accordion-down": "accordion-down 0.2s ease-out",
        "accordion-up": "accordion-up 0.2s ease-out",
        "badge-glow": "badge-glow 3s ease-in-out infinite",
        "badge-shimmer-platinum": "badge-shimmer-platinum 2s ease-in-out infinite",
        "badge-shimmer-diamond": "badge-shimmer-diamond 2s ease-in-out infinite",
        "badge-shimmer-super": "badge-shimmer-super 2s ease-in-out infinite",
        "fade-in": "fade-in 0.5s ease-out",
        "fade-in-subtle": "fade-in 0.25s ease-out",
        "fade-in-up": "fade-in-up 0.5s cubic-bezier(0.25, 0.1, 0.25, 1) forwards",
        "fade-in-up-delay-1": "fade-in-up 0.5s cubic-bezier(0.25, 0.1, 0.25, 1) 0.1s forwards",
        "fade-in-up-delay-2": "fade-in-up 0.5s cubic-bezier(0.25, 0.1, 0.25, 1) 0.2s forwards",
        "fade-in-up-delay-3": "fade-in-up 0.5s cubic-bezier(0.25, 0.1, 0.25, 1) 0.3s forwards",
        "fade-in-up-delay-4": "fade-in-up 0.5s cubic-bezier(0.25, 0.1, 0.25, 1) 0.4s forwards",
        "fade-in-up-delayed": "fade-in-up 0.5s cubic-bezier(0.25, 0.1, 0.25, 1) 0.1s forwards",
        "fire-glow": "fire-glow 2s ease-in-out infinite",
        "hardcore-pulse": "hardcore-pulse 2s ease-in-out infinite",
        "lightbulb-glow": "lightbulb-glow 2s ease-in-out infinite",
        loading: "loading 2.5s linear infinite",
        "prism-face-fade-out": "prism-face-fade-out 600ms linear both",
        "prism-roll": "prism-roll 600ms cubic-bezier(0.16, 1, 0.3, 1) both",
        "pulse-text": "pulse-text 2.5s cubic-bezier(0.4, 0, 0.6, 1) infinite",
        sheen: "sheen 3s cubic-bezier(0.16, 1, 0.3, 1) infinite",
        "slide-in-up-subtle": "slide-in-up-subtle 0.3s cubic-bezier(0.25, 0.1, 0.25, 1) forwards",
        "slide-infinite": "slide 2s infinite",
        sparkle: "sparkle 1.2s ease-in-out infinite",
      },
      // Full-color roles deliberately omit alpha placeholders: choose a named state, not /opacity.
      backgroundColor: {
        "wv-diff-added": "var(--wv-bg-diff-added)",
        "wv-diff-removed": "var(--wv-bg-diff-removed)",
        "wv-signup-page": "var(--wv-bg-signup-page)",
        "wv-signup-card": "var(--wv-bg-signup-card)",
        "wv-signup-app-brand": "var(--wv-bg-signup-app-brand)",
        "wv-signup-app-action": "var(--wv-bg-signup-app-action)",
        "wv-signup-app-action-hover": "var(--wv-bg-signup-app-action-hover)",
        "wv-signup-router-action": "var(--wv-bg-signup-router-action)",
        "wv-signup-router-action-hover": "var(--wv-bg-signup-router-action-hover)",
        "wv-signup-app-preview": "var(--wv-bg-signup-app-preview)",
        "wv-signup-router-preview": "var(--wv-bg-signup-router-preview)",
        "wv-signup-app-orbit": "var(--wv-bg-signup-app-orbit)",
        "wv-signup-glass-badge": "var(--wv-bg-signup-glass-badge)",
        "wv-signup-glass-icon": "var(--wv-bg-signup-glass-icon)",
        "wv-signin-panel": "var(--wv-bg-signin-panel)",
        "wv-signin-panel-logo-tile": "var(--wv-bg-signin-panel-logo-tile)",
        "wv-import-brand": "var(--wv-bg-import-brand)",
        "wv-import-hover": "var(--wv-bg-import-hover)",
        "wv-import-attention": "var(--wv-bg-import-attention)",
        "wv-import-subtle": "var(--wv-bg-import-subtle)",
        "wv-action": "var(--wv-bg-action)",
        "wv-action-hover": "var(--wv-bg-action-hover)",
        "wv-action-subtle": "var(--wv-bg-action-subtle)",
        "wv-action-subtle-active": "var(--wv-bg-action-subtle-active)",
        "wv-action-subtle-hover": "var(--wv-bg-action-subtle-hover)",
        "wv-active": "var(--wv-bg-active)",
        "wv-disabled": "var(--wv-bg-disabled)",
        "wv-divider": "var(--wv-bg-divider)",
        "wv-field": "var(--wv-bg-field)",
        "wv-hover": "var(--wv-bg-hover)",
        "wv-inset": "var(--wv-bg-inset)",
        "wv-keycap": "var(--wv-bg-keycap)",
        "wv-frame": "var(--wv-layer-frame)",
        "wv-neutral-solid": "var(--wv-bg-neutral-solid)",
        "wv-neutral-solid-hover": "var(--wv-bg-neutral-solid-hover)",
        "wv-neutral-action-hover": "var(--wv-bg-neutral-action-hover)",
        "wv-neutral-action-active": "var(--wv-bg-neutral-action-active)",
        "wv-on-neutral-solid": "var(--wv-bg-on-neutral-solid)",
        "wv-placeholder": "var(--wv-bg-placeholder)",
        "wv-track": "var(--wv-bg-track)",
        "wv-switch-thumb-on": "var(--wv-bg-switch-thumb-on)",
        "wv-switch-thumb-off": "var(--wv-bg-switch-thumb-off)",
        "wv-scrim": "var(--wv-bg-scrim)",
        "wv-selected": "var(--wv-bg-selected)",
        "wv-status-danger": "var(--wv-bg-status-danger)",
        "wv-status-danger-active": "var(--wv-bg-status-danger-active)",
        "wv-status-danger-solid": "var(--wv-bg-status-danger-solid)",
        "wv-status-danger-solid-hover": "var(--wv-bg-status-danger-solid-hover)",
        "wv-status-danger-subtle": "var(--wv-bg-status-danger-subtle)",
        "wv-status-info": "var(--wv-bg-status-info)",
        "wv-status-info-active": "var(--wv-bg-status-info-active)",
        "wv-status-info-solid": "var(--wv-bg-status-info-solid)",
        "wv-status-info-solid-hover": "var(--wv-bg-status-info-solid-hover)",
        "wv-status-info-subtle": "var(--wv-bg-status-info-subtle)",
        "wv-status-neutral": "var(--wv-bg-status-neutral)",
        "wv-status-neutral-active": "var(--wv-bg-status-neutral-active)",
        "wv-status-neutral-solid": "var(--wv-bg-status-neutral-solid)",
        "wv-status-neutral-solid-hover": "var(--wv-bg-status-neutral-solid-hover)",
        "wv-status-neutral-subtle": "var(--wv-bg-status-neutral-subtle)",
        "wv-status-success": "var(--wv-bg-status-success)",
        "wv-status-success-active": "var(--wv-bg-status-success-active)",
        "wv-status-success-solid": "var(--wv-bg-status-success-solid)",
        "wv-status-success-solid-hover": "var(--wv-bg-status-success-solid-hover)",
        "wv-status-success-subtle": "var(--wv-bg-status-success-subtle)",
        "wv-status-warning": "var(--wv-bg-status-warning)",
        "wv-status-warning-active": "var(--wv-bg-status-warning-active)",
        "wv-status-warning-layered": "var(--wv-bg-status-warning-layered)",
        "wv-status-warning-layered-hover": "var(--wv-bg-status-warning-layered-hover)",
        "wv-status-warning-solid": "var(--wv-bg-status-warning-solid)",
        "wv-status-warning-solid-hover": "var(--wv-bg-status-warning-solid-hover)",
        "wv-status-warning-subtle": "var(--wv-bg-status-warning-subtle)",
        "wv-surface": "var(--wv-bg-surface)",
        "wv-tab-active": "var(--wv-bg-tab-active)",
      },
      borderColor: {
        "wv-signup-glass-badge": "var(--wv-border-signup-glass-badge)",
        "wv-signup-glass-icon": "var(--wv-border-signup-glass-icon)",
        "wv-signup-glass-orbit": "var(--wv-border-signup-glass-orbit)",
        "wv-signin-panel-divider": "var(--wv-border-signin-panel-divider)",
        "wv-import-default": "var(--wv-border-import-default)",
        "wv-import-muted": "var(--wv-border-import-muted)",
        "wv-import-hover": "var(--wv-border-import-hover)",
        "wv-action": "var(--wv-border-action)",
        "wv-action-subtle": "var(--wv-border-action-subtle)",
        "wv-action-solid": "var(--wv-border-action-solid)",
        "wv-default": "var(--wv-border-default)",
        "wv-focus": "var(--wv-border-focus)",
        "wv-hover": "var(--wv-border-hover)",
        "wv-neutral-solid": "var(--wv-bg-neutral-solid)",
        "wv-status-danger": "var(--wv-border-status-danger)",
        "wv-status-danger-subtle": "var(--wv-border-status-danger-subtle)",
        "wv-status-info": "var(--wv-border-status-info)",
        "wv-status-info-subtle": "var(--wv-border-status-info-subtle)",
        "wv-status-neutral": "var(--wv-border-status-neutral)",
        "wv-status-neutral-subtle": "var(--wv-border-status-neutral-subtle)",
        "wv-status-success": "var(--wv-border-status-success)",
        "wv-status-success-subtle": "var(--wv-border-status-success-subtle)",
        "wv-status-warning": "var(--wv-border-status-warning)",
        "wv-status-warning-subtle": "var(--wv-border-status-warning-subtle)",
        "wv-subtle": "var(--wv-border-subtle)",
        "wv-surface": "var(--wv-bg-surface)",
      },
      boxShadow: {
        "wv-chart-mark-depth": "var(--wv-shadow-chart-mark-depth)",
        "wv-composer": "var(--wv-shadow-composer)",
        "wv-composer-raised": "var(--wv-shadow-composer-raised)",
        "wv-signup-card": "var(--wv-shadow-signup-card)",
        "wv-signup-card-hover": "var(--wv-shadow-signup-card-hover)",
        "wv-signup-glass-icon": "var(--wv-shadow-signup-glass-icon)",
        "wv-workspace": "var(--wv-shadow-workspace)",
        "fake-border": "inset 0 -1px 0 var(--wv-legacy-border, hsl(var(--border)))",
      },
      divideColor: {
        "wv-default": "var(--wv-border-default)",
      },
      fill: {
        current: "currentColor",
      },
      stroke: {
        current: "currentColor",
      },
      transitionDuration: MOTION_DURATION_THEME,
      transitionTimingFunction: MOTION_EASING_THEME,
      fontFamily: {
        display: ["var(--font-geist)"],
        sans: ["var(--font-geist)"],
      },
      fontSize: {
        "2xs": ["0.75rem", "0.875rem"],
        xs: ["0.8125rem", "1rem"],
        ...TYPOGRAPHY_FONT_SIZE_THEME,
      },
      fontWeight: {
        thin: "var(--font-weight-ui)",
        extralight: "var(--font-weight-ui)",
        light: "var(--font-weight-ui)",
        normal: "var(--font-weight-ui)",
        ui: "var(--font-weight-ui)",
        navigation: "var(--font-weight-navigation)",
        medium: "var(--font-weight-emphasis)",
        semibold: "var(--font-weight-emphasis)",
        bold: "var(--font-weight-emphasis)",
        extrabold: "var(--font-weight-emphasis)",
        black: "var(--font-weight-emphasis)",
      },
      gradientColorStops: {
        "wv-surface": "var(--wv-bg-surface)",
      },
      keyframes: {
        "accordion-down": {
          from: { height: "0" },
          to: { height: "var(--radix-accordion-content-height)" },
        },
        "accordion-up": {
          from: { height: "var(--radix-accordion-content-height)" },
          to: { height: "0" },
        },
        "badge-glow": {
          "0%, 100%": {
            color: "hsl(280 70% 55%)", // Purple
            filter: "drop-shadow(0 0 4px hsl(280 70% 55% / 0.6))",
          },
          "33%": {
            color: "hsl(200 80% 50%)", // Cyan/teal
            filter: "drop-shadow(0 0 4px hsl(200 80% 50% / 0.6))",
          },
          "66%": {
            color: "hsl(340 70% 55%)", // Rose/pink
            filter: "drop-shadow(0 0 4px hsl(340 70% 55% / 0.6))",
          },
        },
        "badge-shimmer-platinum": {
          "0%, 100%": {
            boxShadow: "0 0 3px var(--wv-achievement-platinum)",
          },
          "50%": {
            boxShadow:
              "0 0 5px var(--wv-achievement-platinum-highlight), 0 0 10px var(--wv-achievement-platinum)",
          },
        },
        "badge-shimmer-diamond": {
          "0%, 100%": {
            boxShadow: "0 0 3px var(--wv-achievement-diamond)",
          },
          "50%": {
            boxShadow:
              "0 0 5px var(--wv-achievement-diamond-highlight), 0 0 10px var(--wv-achievement-diamond)",
          },
        },
        "badge-shimmer-super": {
          "0%, 100%": {
            boxShadow: "0 0 3px var(--wv-achievement-super-border)",
          },
          "50%": {
            boxShadow:
              "0 0 5px var(--wv-achievement-super-highlight), 0 0 10px var(--wv-achievement-super-border)",
          },
        },
        "fade-in": {
          from: { opacity: "0" },
          to: { opacity: "1" },
        },
        "fade-in-up": {
          from: {
            opacity: "0",
            transform: "translateY(-20px)",
          },
          to: {
            opacity: "1",
            transform: "translateY(0)",
          },
        },
        "fire-glow": {
          "0%, 100%": {
            color: "hsl(var(--warning))",
            textShadow: [
              "0 0 10px hsl(var(--warning) / 0.8)",
              "0 0 20px hsl(var(--warning) / 0.6)",
              "0 0 30px hsl(var(--warning) / 0.4)",
              "0 0 40px hsl(var(--warning) / 0.2)",
            ].join(", "),
          },
          "33%": {
            color: "hsl(var(--info))",
            textShadow: [
              "0 0 15px hsl(var(--info) / 0.9)",
              "0 0 25px hsl(var(--info) / 0.7)",
              "0 0 35px hsl(var(--info) / 0.5)",
              "0 0 45px hsl(var(--info) / 0.3)",
            ].join(", "),
          },
          "66%": {
            color: "hsl(var(--danger))",
            textShadow: [
              "0 0 12px hsl(var(--danger) / 0.9)",
              "0 0 24px hsl(var(--danger) / 0.7)",
              "0 0 36px hsl(var(--danger) / 0.5)",
              "0 0 48px hsl(var(--danger) / 0.3)",
            ].join(", "),
          },
        },
        "hardcore-pulse": {
          "0%, 100%": {
            color: "hsl(250 95% 70%)",
            textShadow: [
              "0 0 10px hsl(250 95% 70% / 0.8)",
              "0 0 20px hsl(250 95% 70% / 0.6)",
              "0 0 30px hsl(250 95% 70% / 0.4)",
            ].join(", "),
          },
          "50%": {
            color: "hsl(290 95% 70%)",
            textShadow: [
              "0 0 15px hsl(290 95% 70% / 0.9)",
              "0 0 25px hsl(290 95% 70% / 0.7)",
              "0 0 35px hsl(290 95% 70% / 0.5)",
            ].join(", "),
          },
        },
        // Pulsing warm-yellow glow for the "Tips" lightbulb affordance. Uses a fixed yellow
        // (not the orange --warning token) so it reads as a friendly lightbulb rather than a
        // warning, and animates filter drop-shadow so it glows without shifting layout.
        "lightbulb-glow": {
          "0%, 100%": {
            filter: "drop-shadow(0 0 2px hsl(48 100% 60% / 0.7))",
          },
          "50%": {
            filter: "drop-shadow(0 0 6px hsl(48 100% 55% / 0.95))",
          },
        },
        loading: {
          to: { transform: "rotate(360deg)" },
        },
        "prism-face-fade-out": {
          "55%, 100%": { opacity: "0" },
          from: { opacity: "1" },
        },
        "prism-roll": {
          from: { transform: "rotateX(0deg)" },
          to: { transform: "rotateX(90deg)" },
        },
        "pulse-text": {
          "0%, 100%": {
            opacity: "0.7",
            textShadow: "0 0 0px rgba(217, 70, 239, 0)",
          },
          "50%": {
            opacity: "1",
            textShadow: "0 0 12px rgba(217, 70, 239, 0.5)",
          },
        },
        sheen: {
          "0%": { transform: "translateX(-120%)" },
          "30%, 100%": { transform: "translateX(120%)" },
        },
        slide: {
          to: { transform: "translateX(100%)" },
        },
        "slide-in-up-subtle": {
          from: {
            opacity: "0",
            transform: "translateY(6px)",
          },
          to: {
            opacity: "1",
            transform: "translateY(0)",
          },
        },
        sparkle: {
          "0%, 100%": {
            opacity: "0",
            transform: "scale(0)",
          },
          "50%": {
            boxShadow: "0 0 4px 1px hsl(270 95% 75% / 0.8)",
            opacity: "1",
            transform: "scale(1)",
          },
        },
      },
      accentColor: {
        "wv-import-brand": "var(--wv-color-import-coral)",
      },
      outlineColor: {
        "wv-signup-app-heading": "var(--wv-text-signup-app-heading)",
        "wv-signup-app-action": "var(--wv-text-signup-app-action)",
        "wv-signup-router-action": "var(--wv-text-signup-router-action)",
        "wv-signup-site-link": "var(--wv-text-signup-site-link)",
        "wv-signin-panel": "var(--wv-text-signin-panel)",
        "wv-focus": "var(--wv-border-focus)",
      },
      ringColor: {
        "wv-default": "var(--wv-border-default)",
        "wv-focus": "var(--wv-border-focus)",
        "wv-status-danger-subtle": "var(--wv-border-status-danger-subtle)",
        "wv-surface": "var(--wv-bg-surface)",
      },
      ringOffsetColor: {
        "wv-surface": "var(--wv-bg-surface)",
      },
      spacing: {
        "content-width": "96rem",
        "text-width": "var(--text-width)",
      },
      textColor: {
        "wv-signup-body": "var(--wv-text-signup-body)",
        "wv-signup-secondary": "var(--wv-text-signup-secondary)",
        "wv-signup-app-heading": "var(--wv-text-signup-app-heading)",
        "wv-signup-router-heading": "var(--wv-text-signup-router-heading)",
        "wv-signup-site-link": "var(--wv-text-signup-site-link)",
        "wv-signup-app-accent": "var(--wv-text-signup-app-accent)",
        "wv-signup-router-accent": "var(--wv-text-signup-router-accent)",
        "wv-signup-app-action": "var(--wv-text-signup-app-action)",
        "wv-signup-router-action": "var(--wv-text-signup-router-action)",
        "wv-signin-panel": "var(--wv-text-signin-panel)",
        "wv-import-brand": "var(--wv-text-import-brand)",
        "wv-import-muted": "var(--wv-text-import-muted)",
        "wv-import-on-brand": "var(--wv-color-stone-50)",
        "wv-action": "var(--wv-text-action)",
        "wv-disabled": "var(--wv-text-disabled)",
        "wv-inverse": "var(--wv-text-inverse)",
        "wv-on-action": "var(--wv-text-on-action)",
        "wv-placeholder": "var(--wv-text-placeholder)",
        "wv-primary": "var(--wv-text-primary)",
        "wv-secondary": "var(--wv-text-secondary)",
        "wv-status-danger": "var(--wv-text-status-danger)",
        "wv-status-danger-on-solid": "var(--wv-text-status-danger-on-solid)",
        "wv-status-info": "var(--wv-text-status-info)",
        "wv-status-info-on-solid": "var(--wv-text-status-info-on-solid)",
        "wv-status-neutral": "var(--wv-text-status-neutral)",
        "wv-status-neutral-on-solid": "var(--wv-text-status-neutral-on-solid)",
        "wv-status-success": "var(--wv-text-status-success)",
        "wv-status-success-on-solid": "var(--wv-text-status-success-on-solid)",
        "wv-status-warning": "var(--wv-text-status-warning)",
        "wv-status-warning-on-solid": "var(--wv-text-status-warning-on-solid)",
        "wv-tertiary": "var(--wv-text-tertiary)",
      },
    },
  },
} satisfies Partial<Config>;
