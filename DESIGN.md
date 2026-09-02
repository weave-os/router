# Router DESIGN.md

This project uses a Router-specific adaptation of the VoltAgent design analysis
from [awesome-design-md](https://github.com/VoltAgent/awesome-design-md),
especially its terminal-native dark canvas, electric-green signal color, and
hairline card system. The source analysis is available at
https://raw.githubusercontent.com/VoltAgent/awesome-design-md/main/design-md/voltagent/DESIGN.md.

The product is an AI model gateway and control plane. Its interface must make
the routing chain legible: request intent → catalog model → provider endpoint →
credential. The dashboard is an operational working surface, not a marketing
landing page.

## Visual direction

- Near-black canvas: `#101010`.
- Soft black surface: `#1a1a1a`.
- Electric green signal: `#00d992`.
- Cool off-white ink: `#f2f2f2`; body text: `#bdbdbd`; muted text: `#8b949e`.
- Hairline border: `#3d3a39`.
- Use green only for active, healthy, selected, or primary actions.
- Prefer flat surfaces and precise 1px borders over heavy shadows.
- Use dashed or dotted rules sparingly to suggest a routing boundary.

## Typography

- Use Inter for UI copy, headings, labels, and buttons.
- Use the system monospace stack for model IDs, provider IDs, endpoint URLs,
  request counts, latency, and install commands.
- Eyebrows are uppercase, compact, and tracked: `ROUTER CONTROL PLANE`.
- Headings are calm and precise; do not use oversized promotional typography in
  the application shell.
- Numeric values use tabular numerals.

## Application shell

- The shell is a dark two-surface layout with a narrow navigation rail/sidebar
  and a scrollable content canvas.
- Keep the Router mark, environment/status indicator, and sign-out/settings
  controls visible in the shell.
- Active navigation uses a subtle soft-black row plus a 2px green edge or dot.
- Navigation labels should describe operator tasks: Overview, Providers,
  Models & routing, and Access.
- On small screens, collapse the sidebar without hiding the current page title
  or the primary action.

## Dashboard composition

The first viewport should expose useful state immediately:

1. A compact hero stating that the Router is the single gateway for prompts.
2. A live status and inventory strip showing provider credentials, routable
   models, and the current routing mode.
3. A dispatch pipeline card showing the three stages: Intent signal, Model
   catalog, Provider endpoint.
4. Cost, latency, request, and model-distribution analysis below the control
   surface.

Use real values from the API whenever available. Empty states should explain
   the next operator action, such as adding a provider credential or serving the
   first request. Never present fake activity as live data.

## Components

### Cards

- Background `#101010` or `#1a1a1a`, 1px `#3d3a39` border, 8px radius, 20–24px
  internal padding.
- Featured/active cards may use a 2px green edge or a small green status dot.
- Avoid rounded-xl/rounded-2xl treatment except for onboarding surfaces that
  need a clear decision boundary.

### Buttons

- Primary: `#00d992` background, `#101010` text, 6px radius, 44px minimum
  touch height.
- Secondary: transparent near-black background with a hairline border.
- Tertiary: green text with no fill.
- Button labels are short and action-oriented: `Add provider`, `View models`,
  `Copy command`.

### Inputs and code

- Inputs use `#1a1a1a` with a hairline border and 6px radius.
- Model names, aliases, base URLs, and CLI commands use monospace.
- Copy affordances belong inside code blocks and must expose a clear success
  state without revealing stored secrets.

### Status

- Healthy/connected: green dot and green text.
- Inactive/unconfigured: muted gray.
- Warning: amber only for actionable degraded state.
- Error: restrained red, never as decorative emphasis.

## Routing-specific language

Prefer concrete operator language:

- “Routable models” means models that survive model/provider exclusions and have
  a usable credential binding.
- “Provider endpoint” means the configured upstream URL and its authentication
  binding, never the raw secret.
- “Intent routing” means the Router derives bounded intent tags from the prompt
  and uses them as a preference signal; it does not expose chain-of-thought.
- “Failover” means another eligible provider/model is selected after a
  classified upstream failure.

## Layout and responsive behavior

- 4px base spacing scale; common gaps are 8, 12, 16, 24, and 32px.
- Content max width is 1536px; preserve readable chart widths.
- Desktop: sidebar + content canvas, two/three-column metric and inventory
  grids.
- Tablet: two-column cards and a compact sidebar.
- Mobile: one-column cards, horizontally scrollable dense tables, 44px touch
  targets, and a collapsible navigation drawer.

## Do / don't

Do:

- Make the provider/model relationship visible.
- Show whether a route is healthy before showing optimization detail.
- Keep secrets masked and show only safe key prefixes/suffixes.
- Use monospace for infrastructure data and normal type for explanation.

Don't:

- Use a generic blue SaaS dashboard palette.
- Put a marketing hero before operational status.
- Claim a provider is active because it is merely present in the catalog.
- Show raw API keys, OAuth tokens, or full request bodies.
- Add gradients, decorative illustrations, or noisy motion to the control plane.

