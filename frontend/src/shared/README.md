# Shared Router dashboard UI

Host-neutral UI used by both Router dashboards: self-hosted (`frontend/src/app`, served at `/ui`) and the hosted product, which compiles these files through its `@router-ui/*` path alias.

## Rules

- Relative imports only. `@/` means a different root in each host, so `oxlint` rejects it here, along with `urql`, `graphql` and anything under a `graphql/` directory.
- Pages read and write through `RouterDashboardDataSource` (`data/`), never through `fetch` or a GraphQL client.
- Host-only UI (credits, plan, subscriptions) goes in a slot (`slots/`); shared code never names it.
- Styling comes from `theme/tailwindPreset.ts` and `theme/tokens.css`. Hosts add the preset and list `src/shared/**/*.{ts,tsx}` in Tailwind `content`.

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

`useRouterDashboardQuery(ds => ds.metrics.summary(range), [range.from, range.to])` returns a `LoadState`, keeps the previous value visible while a new range loads, and drops responses from superseded calls.

Wire types live in `data/types.ts`. They match `/admin/v1` responses, so the self-hosted adapter (`src/lib/selfHostedDataSource.ts`) passes them through and the hosted adapter maps GraphQL results into them.
