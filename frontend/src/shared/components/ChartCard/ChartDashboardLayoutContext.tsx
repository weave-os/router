"use client";

import { createContext, type PropsWithChildren, useContext } from "react";

interface ChartDashboardLayoutContextValue {
  /** When true, chart content should fill the resizable grid tile height. */
  fillHeight: boolean;
}

const ChartDashboardLayoutContext = createContext<ChartDashboardLayoutContextValue>({
  fillHeight: false,
});

export function ChartDashboardLayoutProvider({
  children,
  fillHeight = true,
}: PropsWithChildren<{ fillHeight?: boolean }>) {
  return (
    <ChartDashboardLayoutContext.Provider value={{ fillHeight }}>
      {children}
    </ChartDashboardLayoutContext.Provider>
  );
}

export function useChartDashboardLayout(): ChartDashboardLayoutContextValue {
  return useContext(ChartDashboardLayoutContext);
}
