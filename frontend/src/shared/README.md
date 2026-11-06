# Shared Router dashboard UI

Host-neutral UI used by both Router dashboards: self-hosted (`frontend/src/app`, served at `/ui`) and the hosted product, which compiles these files through its `@router-ui/*` path alias.

## Rules

- Relative imports only. `@/` means a different root in each host, so `oxlint` rejects it here, along with `urql`, `graphql` and anything under a `graphql/` directory.
- Pages read and write through `RouterDashboardDataSource` (`data/`), never through `fetch` or a GraphQL client.
- Host-only UI (credits, plan, subscriptions) goes in a slot (`slots/`); shared code never names it.
- Generic design-system code (atoms, molecules, charts, chart cards, generic tools, theme, motion) comes from the `@weave-os/ui` npm package, which is published from the Weave repo. Only Router product UI lives here.
- Styling comes from `@weave-os/ui/theme/tailwindPreset` and `@weave-os/ui/theme/tokens.css`. Hosts add the preset and list `src/shared/**/*.{ts,tsx}` and `node_modules/@weave-os/ui/**/*.js` in Tailwind `content`.

## Data source

```tsx
<RouterDashboardDataProvider dataSource={selfHostedDataSource}>
  <RouterDashboardSlotsProvider
    slots={{ [RouterDashboardSlotName.DashboardAside]: <CreditsPanel /> }}
  >
    <DashboardPage />
  </RouterDashboardSlotsProvider>
</RouterDashboardDataProvider>
```

`RouterDashboardDataSource` has four sections: `metrics` (summary, cost timeseries, model mix), `individuals` (rows keyed by user email), `requests` (request log) and `settings` (`models`, `providers`, `routing`, `apiKeys`, `providerKeys`, `usage`). Every section except `metrics` is a `Capability<T>`: either the implementation or `unavailable(reason, message)`. Pages check `isUnavailable(capability)` and render the message instead of calling a missing endpoint.

`useRouterDashboardQuery(dataSource => dataSource.metrics.summary(range), [range.from, range.to])` returns a `LoadState`, keeps the previous value visible while a new range loads, and drops responses from superseded calls.

Wire types live in `data/types.ts`. They match `/admin/v1` responses, so the self-hosted adapter (`src/lib/selfHostedDataSource.ts`) passes them through and the hosted adapter maps GraphQL results into them.
