import React, { useContext } from "react";

import { once } from "../../tools/once";
import { ChartConfig, ChartDataKeyType, ChartDataValueType } from "./types";

interface ChartContext<
  TDependentKey extends ChartDataKeyType,
  TIndependentValue extends ChartDataValueType,
  TDependentValue extends ChartDataValueType,
> {
  /**
   * The series that the user is currently hovering over, if any.
   */
  activeSeries: TDependentKey | undefined;
  config: ChartConfig<TDependentKey, TIndependentValue, TDependentValue> | undefined;
  /**
   * The set of series that the user has hidden by clicking on the legend.
   * Only present for chart types that support toggling series visibility.
   */
  hiddenSeries?: ReadonlySet<TDependentKey>;
  /**
   * When true, a hidden series is removed from the legend entirely instead of
   * rendering dimmed. Only safe when another control (e.g. a series visibility
   * dropdown) remains available to re-show it — otherwise the legend's own
   * click-to-toggle is the only way back.
   *
   * @default false
   */
  hideHiddenSeriesFromLegend?: boolean;
  setActiveSeries: React.Dispatch<React.SetStateAction<TDependentKey | undefined>>;
  /**
   * Toggle a series' visibility. If `exclusive` is true, hide all series except the
   * clicked one (solo mode). If the clicked series is already the only visible one,
   * restore all series. Only present for chart types that support toggling series visibility.
   */
  toggleSeriesVisibility?: (key: TDependentKey, exclusive?: boolean) => void;
}

// We use a callback here instead of directly exporting the `React.CreateContext` result so we can
// add the type parameters.
export const getChartContext = once(
  <
    TDependentKey extends ChartDataKeyType,
    TIndependentValue extends ChartDataValueType,
    TDependentValue extends ChartDataValueType,
  >() =>
    React.createContext<ChartContext<TDependentKey, TIndependentValue, TDependentValue> | null>(
      null,
    ),
);

export function useChartContext<
  TDependentKey extends ChartDataKeyType,
  TIndependentValue extends ChartDataValueType,
  TDependentValue extends ChartDataValueType,
>(): ChartContext<TDependentKey, TIndependentValue, TDependentValue> {
  const context = useContext(getChartContext<TDependentKey, TIndependentValue, TDependentValue>());

  if (context == null) {
    throw new Error("useChartContext must be used within a ChartContext.Provider");
  }

  return context;
}
