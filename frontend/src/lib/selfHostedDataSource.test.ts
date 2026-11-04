import { isUnavailable } from "@/shared/data";

import { selfHostedDataSource } from "./selfHostedDataSource";

const fetchMock = vi.fn();

beforeEach(() => {
  fetchMock.mockResolvedValue(
    new Response(JSON.stringify({ keys: [], rows: [] }), { status: 200 }),
  );
  vi.stubGlobal("fetch", fetchMock);
});

afterEach(() => {
  vi.unstubAllGlobals();
});

function requestedURL() {
  return new URL(String(fetchMock.mock.lastCall?.[0]), "http://router.test");
}

test("requests list reads /admin/v1/metrics/details for the range", async () => {
  const { requests } = selfHostedDataSource;
  if (isUnavailable(requests)) throw new Error("requests should be available");

  await requests.list({ limit: 50, range: { from: "2026-01-01", to: "2026-01-02" } });

  const url = requestedURL();
  expect(url.pathname).toBe("/admin/v1/metrics/details");
  expect(Object.fromEntries(url.searchParams)).toEqual({
    from: "2026-01-01",
    limit: "50",
    to: "2026-01-02",
  });
});

test("API key list unwraps the response envelope", async () => {
  const { apiKeys } = selfHostedDataSource.settings;
  if (isUnavailable(apiKeys)) throw new Error("api keys should be available");

  await expect(apiKeys.list()).resolves.toEqual([]);
  expect(requestedURL().pathname).toBe("/admin/v1/keys");
});

test("capabilities without an /admin/v1 endpoint are declared unavailable", () => {
  expect(isUnavailable(selfHostedDataSource.individuals)).toBe(true);
  expect(isUnavailable(selfHostedDataSource.settings.usage)).toBe(true);
});

test("subscription pool reads /admin/v1/subscription-pool and maps each seat", async () => {
  fetchMock.mockResolvedValue(
    new Response(
      JSON.stringify({
        enabled: true,
        seats: [
          {
            cooldown_until: null,
            display_name: "Platform seat",
            enabled: true,
            id: "seat-1",
            limit_windows: [
              { resets_at: "2026-11-05T00:00:00Z", used_fraction: 0.4, window_minutes: 10080 },
            ],
            limits_observed_at: "2026-11-04T00:00:00Z",
            owner_name: "ada@example.com",
            provider: "claude",
            served_requests: 12,
            state: "active",
          },
        ],
      }),
      { status: 200 },
    ),
  );
  const { subscriptions } = selfHostedDataSource;
  if (isUnavailable(subscriptions)) throw new Error("subscriptions should be available");

  const pool = await subscriptions.pool();

  expect(requestedURL().pathname).toBe("/admin/v1/subscription-pool");
  expect(pool.seats[0]).toMatchObject({
    displayName: "Platform seat",
    limitWindows: [{ resetsAt: "2026-11-05T00:00:00Z", usedFraction: 0.4, windowMinutes: 10080 }],
    ownerName: "ada@example.com",
    servedRequests: 12,
  });
});
