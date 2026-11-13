import { renderHook, act } from "@testing-library/react";

import { LoadState } from "../../tools/LoadState";
import { useToggleSeriesVisibility } from "./useToggleSeriesVisibility";

test("hides projected and partial companions with their actual series", () => {
  const { result } = renderHook(() =>
    useToggleSeriesVisibility({
      dependentKeys: LoadState.loaded([
        "total",
        "feature",
        "__partial__total",
        "__proj__total",
        "__proj__feature",
      ]),
    }),
  );

  act(() => result.current.toggleSeriesVisibility("total"));
  expect(result.current.hiddenSeries.has("total")).toBe(true);
  expect(result.current.hiddenSeries.has("__partial__total")).toBe(true);
  expect(result.current.hiddenSeries.has("__proj__total")).toBe(true);
  expect(result.current.hiddenSeries.has("__proj__feature")).toBe(false);

  act(() => result.current.toggleSeriesVisibility("total"));
  act(() => result.current.toggleSeriesVisibility("feature", true));
  expect(result.current.hiddenSeries.has("__proj__total")).toBe(true);
  expect(result.current.hiddenSeries.has("__proj__feature")).toBe(false);
  act(() => result.current.toggleSeriesVisibility("feature", true));
  expect(result.current.hiddenSeries.size).toBe(0);
});
