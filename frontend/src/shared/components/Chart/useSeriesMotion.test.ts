import { act, renderHook } from "@testing-library/react";

import { vi } from "vitest";

import { LoadState } from "../../tools/LoadState";
import {
  getSeriesStaggerStepMs,
  SERIES_ENTER_DURATION_MS,
  SERIES_EXIT_DURATION_MS,
  SeriesMotionPhase,
  useSeriesMotion,
} from "./useSeriesMotion";

const LOADED_ROWS = [{ count: 1 }];
const FRESH_ROWS = [{ count: 2 }];

type Rows = typeof LOADED_ROWS;

function renderSeriesMotion(initialRows: LoadState<Rows>) {
  return renderHook(({ rows }) => useSeriesMotion(rows, true), {
    initialProps: { rows: initialRows },
  });
}

function preferReducedMotion() {
  vi.spyOn(window, "matchMedia").mockImplementation(query => ({
    addEventListener: () => {},
    addListener: () => {},
    dispatchEvent: () => false,
    matches: true,
    media: query,
    onchange: null,
    removeEventListener: () => {},
    removeListener: () => {},
  }));
}

beforeEach(() => {
  vi.useFakeTimers();
});

afterEach(() => {
  vi.useRealTimers();
  vi.restoreAllMocks();
});

test("rows cached on mount still enter", () => {
  const { result } = renderSeriesMotion(LoadState.loaded(LOADED_ROWS));
  expect(result.current.phase).toBe(SeriesMotionPhase.Entering);

  act(() => {
    vi.advanceTimersByTime(SERIES_ENTER_DURATION_MS);
  });
  expect(result.current.phase).toBe(SeriesMotionPhase.Shown);
  expect(result.current.isAnimationActive).toBe(true);
});

test("rows loaded offscreen wait to enter until the plot mounts", () => {
  const rows = LoadState.loaded(LOADED_ROWS);
  const { rerender, result } = renderHook(
    ({ isPlotMounted }) => useSeriesMotion(rows, isPlotMounted),
    { initialProps: { isPlotMounted: false } },
  );
  act(() => {
    vi.advanceTimersByTime(SERIES_ENTER_DURATION_MS);
  });
  expect(result.current.phase).toBe(SeriesMotionPhase.Hidden);

  rerender({ isPlotMounted: true });
  expect(result.current.phase).toBe(SeriesMotionPhase.Entering);
});

test("rows cached on mount show without an entrance under reduced motion", () => {
  preferReducedMotion();
  const { result } = renderSeriesMotion(LoadState.loaded(LOADED_ROWS));
  expect(result.current.phase).toBe(SeriesMotionPhase.Shown);
  expect(result.current.isAnimationActive).toBe(false);
});

test("rows that finish loading enter, then animate later updates", () => {
  const { rerender, result } = renderSeriesMotion(LoadState.loading());
  expect(result.current.phase).toBe(SeriesMotionPhase.Hidden);

  rerender({ rows: LoadState.loaded(LOADED_ROWS) });
  expect(result.current.phase).toBe(SeriesMotionPhase.Entering);

  act(() => {
    vi.advanceTimersByTime(SERIES_ENTER_DURATION_MS);
  });
  expect(result.current.phase).toBe(SeriesMotionPhase.Shown);
  expect(result.current.isAnimationActive).toBe(true);
});

test("a reload exits from the loaded rows, then reveals the fresh rows", () => {
  const { rerender, result } = renderSeriesMotion(LoadState.loaded(LOADED_ROWS));

  rerender({ rows: LoadState.reloading(LOADED_ROWS) });
  expect(result.current.phase).toBe(SeriesMotionPhase.Exiting);
  expect(result.current.exitingRows).toBe(LOADED_ROWS);

  rerender({ rows: LoadState.loaded(FRESH_ROWS) });
  expect(result.current.phase).toBe(SeriesMotionPhase.Exiting);
  expect(result.current.exitingRows).toBe(LOADED_ROWS);

  act(() => {
    vi.advanceTimersByTime(SERIES_EXIT_DURATION_MS);
  });
  expect(result.current.phase).toBe(SeriesMotionPhase.Entering);
});

test("the exit waits for every staggered point to finish", () => {
  const rows = Array.from({ length: 4 }, (_, count) => ({ count }));
  const { rerender, result } = renderSeriesMotion(LoadState.loaded(rows));

  rerender({ rows: LoadState.reloading(rows) });
  expect(result.current.staggerStepMs).toBe(60);
  expect(result.current.staggerSpanMs).toBe(180);

  act(() => {
    vi.advanceTimersByTime(SERIES_EXIT_DURATION_MS + 179);
  });
  expect(result.current.phase).toBe(SeriesMotionPhase.Exiting);

  act(() => {
    vi.advanceTimersByTime(1);
  });
  expect(result.current.phase).not.toBe(SeriesMotionPhase.Exiting);
});

test("dense charts compress the stagger step to cap the sequence", () => {
  expect(getSeriesStaggerStepMs(1)).toBe(0);
  expect(getSeriesStaggerStepMs(5)).toBe(60);
  expect(getSeriesStaggerStepMs(33)).toBe(15);
});

test("stale rows are not revealed again while the reload is still pending", () => {
  const { rerender, result } = renderSeriesMotion(LoadState.loaded(LOADED_ROWS));

  rerender({ rows: LoadState.reloading(LOADED_ROWS) });
  act(() => {
    vi.advanceTimersByTime(SERIES_EXIT_DURATION_MS);
  });
  expect(result.current.phase).toBe(SeriesMotionPhase.Hidden);

  rerender({ rows: LoadState.loaded(FRESH_ROWS) });
  expect(result.current.phase).toBe(SeriesMotionPhase.Entering);
});

test("rows lost to a fresh load exit from their last loaded value", () => {
  const { rerender, result } = renderSeriesMotion(LoadState.loaded(LOADED_ROWS));

  rerender({ rows: LoadState.loading() });
  expect(result.current.phase).toBe(SeriesMotionPhase.Exiting);
  expect(result.current.exitingRows).toBe(LOADED_ROWS);
});

test("a ready-to-ready change swaps rows without animating", () => {
  const { rerender, result } = renderSeriesMotion(LoadState.loading());
  rerender({ rows: LoadState.loaded(LOADED_ROWS) });
  act(() => {
    vi.advanceTimersByTime(SERIES_ENTER_DURATION_MS);
  });

  rerender({ rows: LoadState.loaded(FRESH_ROWS) });
  expect(result.current.phase).toBe(SeriesMotionPhase.Shown);
  expect(result.current.isAnimationActive).toBe(false);
});

test("turning on reduced motion mid-exit stops the exit and keeps the stale chart", () => {
  let prefersReducedMotion = false;
  let notifyPreferenceChange: EventListenerOrEventListenerObject = () => {};
  vi.spyOn(window, "matchMedia").mockImplementation(query => ({
    addEventListener: (_type: string, listener: EventListenerOrEventListenerObject) => {
      notifyPreferenceChange = listener;
    },
    addListener: () => {},
    dispatchEvent: () => false,
    matches: prefersReducedMotion,
    media: query,
    onchange: null,
    removeEventListener: () => {},
    removeListener: () => {},
  }));
  const { rerender, result } = renderSeriesMotion(LoadState.loaded(LOADED_ROWS));

  rerender({ rows: LoadState.reloading(LOADED_ROWS) });
  expect(result.current.phase).toBe(SeriesMotionPhase.Exiting);

  prefersReducedMotion = true;
  act(() => {
    if (typeof notifyPreferenceChange === "function") notifyPreferenceChange(new Event("change"));
  });
  expect(result.current.phase).toBe(SeriesMotionPhase.Shown);
  expect(result.current.isAnimationActive).toBe(false);
});

test("reduced motion keeps the stale chart in place and reveals without animation", () => {
  preferReducedMotion();
  const { rerender, result } = renderSeriesMotion(LoadState.loaded(LOADED_ROWS));

  rerender({ rows: LoadState.reloading(LOADED_ROWS) });
  expect(result.current.phase).toBe(SeriesMotionPhase.Shown);

  rerender({ rows: LoadState.loading() });
  rerender({ rows: LoadState.loaded(FRESH_ROWS) });
  expect(result.current.phase).toBe(SeriesMotionPhase.Shown);
  expect(result.current.isAnimationActive).toBe(false);
});
