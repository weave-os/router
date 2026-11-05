import { act, cleanup, fireEvent, render, screen, within } from "@testing-library/react";

import {
  RouterDashboardDataProvider,
  type RouterDashboardDataSource,
  RouterForbiddenError,
  type RouterSubscriptionPool as Pool,
  type RouterSubscriptionPoolSeat as PoolSeat,
  RouterSubscriptionProvider,
  RouterSubscriptionSeatState,
  unavailable,
  UnavailableReason,
} from "../../data";
import { TooltipProvider } from "../molecules/Tooltip";
import { RouterSubscriptionPool } from "./RouterSubscriptionPool";

const NOW = Date.parse("2026-11-02T12:00:00Z");
const HOUR_MS = 60 * 60 * 1000;
const SESSION_MINUTES = 300;
const WEEKLY_MINUTES = 10080;

const poolQuery: { enabled: boolean; error?: Error; seats: PoolSeat[] } = {
  enabled: true,
  seats: [],
};

function dataSource(): RouterDashboardDataSource {
  const notSupported = unavailable(UnavailableReason.NotSupportedByHost, "n/a");
  return {
    drillDown: notSupported,
    individuals: notSupported,
    metrics: {
      costTimeseries: vi.fn(),
      modelMix: vi.fn(),
      people: vi.fn(),
      reportSummary: vi.fn(),
      series: vi.fn(),
      summary: vi.fn(),
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
    subscriptions: {
      pool: () =>
        poolQuery.error == null
          ? Promise.resolve<Pool>({ enabled: poolQuery.enabled, seats: poolQuery.seats })
          : Promise.reject(poolQuery.error),
    },
  };
}

function isoAtHoursFromNow(offsetHours: number) {
  return new Date(NOW + offsetHours * HOUR_MS).toISOString();
}

function limitWindow(windowMinutes: number, usedFraction: number, resetsInHours: number) {
  return {
    resetsAt: isoAtHoursFromNow(resetsInHours),
    usedFraction: usedFraction,
    windowMinutes: windowMinutes,
  };
}

function seat(overrides: Partial<PoolSeat> & Pick<PoolSeat, "id" | "displayName">): PoolSeat {
  return {
    cooldownUntil: null,
    enabled: true,
    limitWindows: [],
    limitsObservedAt: isoAtHoursFromNow(-1),
    ownerName: "Ada Lovelace",
    provider: RouterSubscriptionProvider.Claude,
    servedRequests: 0,
    state: RouterSubscriptionSeatState.Active,
    ...overrides,
  };
}

async function renderPool() {
  render(
    <RouterDashboardDataProvider dataSource={dataSource()}>
      <TooltipProvider>
        <RouterSubscriptionPool />
      </TooltipProvider>
    </RouterDashboardDataProvider>,
  );
  await act(async () => {});
}

function seatRow(name: string) {
  const row = screen.getByText(name).closest("tr");
  if (row == null) throw new Error(`No row for ${name}`);
  return within(row);
}

beforeEach(() => {
  vi.useFakeTimers({ now: NOW, toFake: ["Date"] });
});

afterEach(() => {
  cleanup();
  poolQuery.enabled = true;
  poolQuery.error = undefined;
  vi.useRealTimers();
});

test("shows each pooled seat's limit left, exhaustion, refills, and seats out of the pool", async () => {
  poolQuery.seats = [
    seat({
      displayName: "Platform seat",
      id: "platform",
      limitWindows: [limitWindow(SESSION_MINUTES, 0.36, 2), limitWindow(WEEKLY_MINUTES, 0.85, 48)],
      servedRequests: 20246,
    }),
    seat({
      displayName: "Infra seat",
      id: "infra",
      limitWindows: [limitWindow(SESSION_MINUTES, 0.8, -3), limitWindow(WEEKLY_MINUTES, 1, 30)],
      servedRequests: 25052,
      state: RouterSubscriptionSeatState.Exhausted,
    }),
    seat({
      displayName: "Codex seat",
      id: "codex",
      limitWindows: [limitWindow(WEEKLY_MINUTES, 0.28, 40)],
      provider: RouterSubscriptionProvider.Codex,
    }),
    seat({
      displayName: "Idle Codex seat",
      id: "idle-codex",
      limitWindows: [],
      limitsObservedAt: null,
      provider: RouterSubscriptionProvider.Codex,
    }),
    seat({
      displayName: "Stale seat",
      id: "stale",
      state: RouterSubscriptionSeatState.ReconnectRequired,
    }),
    seat({
      displayName: "Switched-off seat",
      enabled: false,
      id: "switched-off",
      state: RouterSubscriptionSeatState.ReconnectRequired,
    }),
  ];

  await renderPool();

  const claudeSummary = within(
    screen.getByRole("region", { name: "Claude Code weekly limit left" }),
  );
  expect(claudeSummary.getByText("8%")).toBeVisible();
  expect(claudeSummary.getByText(/1 of 2 seats serving/)).toBeVisible();

  const claudeTable = within(screen.getAllByRole("table")[0]);
  expect(claudeTable.getAllByRole("columnheader").map(header => header.textContent)).toEqual([
    "Seat",
    "Session limit",
    "Weekly limit",
    "Requests (7 days)",
  ]);
  expect(claudeTable.getByRole("rowheader", { name: /Platform seat/ })).toBeVisible();

  const platform = seatRow("Platform seat");
  expect(platform.getByText(/64% left · resets in 2 hours/)).toBeVisible();
  expect(platform.getByText(/15% left · resets in 2 days/)).toBeVisible();
  expect(platform.getByText("20,246")).toBeVisible();

  const infra = seatRow("Infra seat");
  expect(infra.getByText("Usage exhausted")).toBeVisible();
  expect(infra.getByText(/Refilled since last reading/)).toBeVisible();
  expect(infra.getByText(/Limit reached · resets in 1 day/)).toBeVisible();

  const codexTable = within(screen.getAllByRole("table")[1]);
  expect(codexTable.getAllByRole("columnheader").map(header => header.textContent)).toEqual([
    "Seat",
    "Weekly limit",
    "Requests (7 days)",
  ]);
  expect(seatRow("Codex seat").getByText(/72% left · resets in 2 days/)).toBeVisible();
  expect(seatRow("Idle Codex seat").getAllByText("No reading in the last 8 days")).toHaveLength(1);

  fireEvent.click(screen.getByRole("button", { name: /Not serving/ }));
  const reconnect = within(screen.getByRole("list", { name: /Reconnect required/ }));
  expect(reconnect.getByText("Stale seat")).toBeVisible();
  const disabled = within(screen.getByRole("list", { name: /Disabled/ }));
  expect(disabled.getByText("Switched-off seat")).toBeVisible();
});

test("explains an empty pool", async () => {
  poolQuery.seats = [];

  await renderPool();

  expect(screen.getByText("No subscriptions in the router pool yet.")).toBeVisible();
});

function barFill(row: ReturnType<typeof seatRow>, caption: RegExp) {
  const fill = row.getByText(caption).previousElementSibling?.firstElementChild;
  if (!(fill instanceof HTMLElement)) throw new Error(`No bar above ${caption}`);
  return fill;
}

test("fills each seat's bar with what is left and warns only when the seat is behind an even burn", async () => {
  poolQuery.seats = [
    seat({
      displayName: "Even seat",
      id: "even",
      limitWindows: [limitWindow(WEEKLY_MINUTES, 0.5, 84)],
    }),
    seat({
      displayName: "Behind seat",
      id: "behind",
      limitWindows: [limitWindow(WEEKLY_MINUTES, 0.9, 120)],
    }),
  ];

  await renderPool();

  const even = barFill(seatRow("Even seat"), /50% left/);
  expect(parseFloat(even.style.width)).toBeCloseTo(50);
  expect(even).not.toHaveClass("bg-wv-status-warning-solid");

  const behind = barFill(seatRow("Behind seat"), /10% left/);
  expect(parseFloat(behind.style.width)).toBeCloseTo(10);
  expect(behind).toHaveClass("bg-wv-status-warning-solid");
  expect(seatRow("Behind seat").getByText(/Behind pace/)).toBeVisible();
  expect(
    seatRow("Behind seat").getByRole("img", { name: /Weekly limit:.*Behind pace/ }),
  ).toBeVisible();
  expect(seatRow("Even seat").queryByText(/Behind pace/)).not.toBeInTheDocument();
});

test("labels each provider's pool as burning fast or on track", async () => {
  poolQuery.seats = [
    seat({
      displayName: "Fast seat",
      id: "fast",
      limitWindows: [limitWindow(WEEKLY_MINUTES, 0.9, 120)],
    }),
    seat({
      displayName: "Slow seat",
      id: "slow",
      limitWindows: [limitWindow(WEEKLY_MINUTES, 0.2, 48)],
      provider: RouterSubscriptionProvider.Codex,
    }),
  ];

  await renderPool();

  const claude = within(screen.getByRole("region", { name: "Claude Code weekly limit left" }));
  expect(claude.getByText("Burning fast")).toBeVisible();
  expect(claude.getByText(/Drops to ~\d+% in/)).toBeVisible();
  const codex = within(screen.getByRole("region", { name: "Codex weekly limit left" }));
  expect(codex.getByText("On track")).toBeVisible();
});

test("lists seats that are out of limit after the ones still serving", async () => {
  poolQuery.seats = [
    seat({
      displayName: "Exhausted seat",
      id: "exhausted",
      limitWindows: [limitWindow(WEEKLY_MINUTES, 1, 30)],
      state: RouterSubscriptionSeatState.Exhausted,
    }),
    seat({
      displayName: "Serving seat",
      id: "serving",
      limitWindows: [limitWindow(WEEKLY_MINUTES, 0.3, 60)],
    }),
  ];

  await renderPool();

  const rowNames = within(screen.getAllByRole("table")[0])
    .getAllByRole("rowheader")
    .map(header => header.textContent);
  expect(rowNames[0]).toContain("Serving seat");
  expect(rowNames[1]).toContain("Exhausted seat");
});

test("says when the router runs without subscription pools", async () => {
  poolQuery.enabled = false;

  await renderPool();

  expect(screen.getByText("Subscription pools are turned off on this router.")).toBeVisible();
});

test("explains a forbidden pool instead of a generic error", async () => {
  poolQuery.error = new RouterForbiddenError();

  await renderPool();

  expect(
    screen.getByText("Only team leads and admins can see the subscription pool."),
  ).toBeVisible();
});
