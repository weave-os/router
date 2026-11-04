import type { RouterDashboardDataSource } from "./RouterDashboardDataSource";
import type { MetricsSummary } from "./types";

import { renderHook, waitFor } from "@testing-library/react";

import { LoadState } from "../tools/LoadState";
import { unavailable, UnavailableReason } from "./availability";
import {
  RouterDashboardDataProvider,
  useRouterDashboardQuery,
} from "./RouterDashboardDataProvider";

function summary(requestCount: number): MetricsSummary {
  return {
    request_count: requestCount,
    total_actual_cost_usd: 0,
    total_requested_cost_usd: 0,
    total_savings_usd: 0,
    total_tokens: 0,
  };
}

function dataSourceWith(summaryFn: RouterDashboardDataSource["metrics"]["summary"]) {
  const notSupported = unavailable(UnavailableReason.NotSupportedByHost, "n/a");
  return {
    individuals: notSupported,
    metrics: {
      costTimeseries: vi.fn(),
      modelMix: vi.fn(),
      reportSummary: vi.fn(),
      series: vi.fn(),
      summary: summaryFn,
    },
    requests: notSupported,
    settings: {
      apiKeys: notSupported,
      models: notSupported,
      providerKeys: notSupported,
      providers: notSupported,
      routing: notSupported,
      usage: notSupported,
    },
  } satisfies RouterDashboardDataSource;
}

function renderQuery(dataSource: RouterDashboardDataSource, initialFrom: string) {
  return renderHook(
    ({ from }) =>
      useRouterDashboardQuery(ds => ds.metrics.summary({ from, to: "2026-01-02" }), [from]),
    {
      initialProps: { from: initialFrom },
      wrapper: ({ children }) => (
        <RouterDashboardDataProvider dataSource={dataSource}>
          {children}
        </RouterDashboardDataProvider>
      ),
    },
  );
}

describe("useRouterDashboardQuery", () => {
  it("loads through the provided data source", async () => {
    const summaryFn = vi.fn().mockResolvedValue(summary(7));
    const { result } = renderQuery(dataSourceWith(summaryFn), "2026-01-01");

    await waitFor(() => expect(result.current).toEqual(LoadState.loaded(summary(7))));
    expect(summaryFn).toHaveBeenCalledWith({ from: "2026-01-01", to: "2026-01-02" });
  });

  it("keeps the previous value while reloading and drops superseded responses", async () => {
    let resolveStale: (value: MetricsSummary) => void = () => {};
    const summaryFn = vi
      .fn()
      .mockResolvedValueOnce(summary(1))
      .mockReturnValueOnce(new Promise<MetricsSummary>(resolve => (resolveStale = resolve)))
      .mockResolvedValueOnce(summary(3));
    const { rerender, result } = renderQuery(dataSourceWith(summaryFn), "a");
    await waitFor(() => expect(result.current).toEqual(LoadState.loaded(summary(1))));

    rerender({ from: "b" });
    expect(result.current).toEqual(LoadState.reloading(summary(1)));
    rerender({ from: "c" });
    await waitFor(() => expect(result.current).toEqual(LoadState.loaded(summary(3))));

    resolveStale(summary(2));
    await Promise.resolve();
    expect(result.current).toEqual(LoadState.loaded(summary(3)));
  });

  it("surfaces failures as an error state", async () => {
    const { result } = renderQuery(
      dataSourceWith(vi.fn().mockRejectedValue(new Error("boom"))),
      "a",
    );
    await waitFor(() => expect(LoadState.isError(result.current)).toBe(true));
    expect(LoadState.isError(result.current) && result.current.error.message).toBe("boom");
  });
});
