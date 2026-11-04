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

function requestedURL(): URL {
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
