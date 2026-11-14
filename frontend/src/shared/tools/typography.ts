/** Semantic text styles. Sizes/line-heights mirror today's scale so callers can swap a
 * legacy size class for its role with no visual change. Weights use the two-weight scale:
 * `--font-weight-ui` (430, the floor) and `--font-weight-emphasis` (500). Sidebar navigation
 * labels use `--font-weight-navigation` (460).
 */
export enum TypographyRole {
  Chart = "chart",
  Caption = "caption",
  Body = "body",
  CardTitle = "card-title",
  SectionTitle = "section-title",
  PageTitle = "page-title",
  Display = "display",
}

interface TypographyRoleToken {
  fontSize: string;
  lineHeight: string;
  /** Omitted for neutral roles so inherited emphasis (e.g. inside a `font-medium` parent) is preserved. */
  fontWeight?: string;
}

const EMPHASIS_WEIGHT = "var(--font-weight-emphasis)";

export const TYPOGRAPHY_ROLE_TOKENS: Record<TypographyRole, TypographyRoleToken> = {
  [TypographyRole.Chart]: { fontSize: "0.75rem", lineHeight: "0.875rem" },
  [TypographyRole.Caption]: { fontSize: "0.8125rem", lineHeight: "1rem" },
  [TypographyRole.Body]: { fontSize: "0.875rem", lineHeight: "1.25rem" },
  [TypographyRole.CardTitle]: {
    fontSize: "1rem",
    lineHeight: "1.5rem",
    fontWeight: EMPHASIS_WEIGHT,
  },
  [TypographyRole.SectionTitle]: {
    fontSize: "1.25rem",
    lineHeight: "1.75rem",
    fontWeight: EMPHASIS_WEIGHT,
  },
  [TypographyRole.PageTitle]: {
    fontSize: "1.5rem",
    lineHeight: "2rem",
    fontWeight: EMPHASIS_WEIGHT,
  },
  [TypographyRole.Display]: {
    fontSize: "2.25rem",
    lineHeight: "2.5rem",
    fontWeight: EMPHASIS_WEIGHT,
  },
};

export const TYPOGRAPHY_FONT_SIZE_THEME: Record<
  TypographyRole,
  [string, { lineHeight: string; fontWeight?: string }]
> = {
  [TypographyRole.Chart]: ["var(--wv-size-chart)", { lineHeight: "var(--wv-leading-chart)" }],
  [TypographyRole.Caption]: ["var(--wv-size-caption)", { lineHeight: "var(--wv-leading-caption)" }],
  [TypographyRole.Body]: ["var(--wv-size-body)", { lineHeight: "var(--wv-leading-body)" }],
  [TypographyRole.CardTitle]: [
    "var(--wv-size-card-title)",
    { lineHeight: "var(--wv-leading-card-title)", fontWeight: "var(--wv-weight-card-title)" },
  ],
  [TypographyRole.SectionTitle]: [
    "var(--wv-size-section-title)",
    {
      lineHeight: "var(--wv-leading-section-title)",
      fontWeight: "var(--wv-weight-section-title)",
    },
  ],
  [TypographyRole.PageTitle]: [
    "var(--wv-size-page-title)",
    { lineHeight: "var(--wv-leading-page-title)", fontWeight: "var(--wv-weight-page-title)" },
  ],
  [TypographyRole.Display]: [
    "var(--wv-size-display)",
    { lineHeight: "var(--wv-leading-display)", fontWeight: "var(--wv-weight-display)" },
  ],
};

export const TYPOGRAPHY_CSS_VARIABLES: Record<string, string> = {
  "--wv-size-chart": "0.75rem",
  "--wv-leading-chart": "0.875rem",
  "--wv-size-caption": "0.8125rem",
  "--wv-leading-caption": "1rem",
  "--wv-size-body": "0.875rem",
  "--wv-leading-body": "1.25rem",
  "--wv-size-card-title": "1rem",
  "--wv-leading-card-title": "1.5rem",
  "--wv-weight-card-title": "var(--font-weight-emphasis)",
  "--wv-size-section-title": "1.25rem",
  "--wv-leading-section-title": "1.75rem",
  "--wv-weight-section-title": "var(--font-weight-emphasis)",
  "--wv-size-page-title": "1.5rem",
  "--wv-leading-page-title": "2rem",
  "--wv-weight-page-title": "var(--font-weight-emphasis)",
  "--wv-size-display": "2.25rem",
  "--wv-leading-display": "2.5rem",
  "--wv-weight-display": "var(--font-weight-emphasis)",
  "--font-weight-emphasis": "500",
};
