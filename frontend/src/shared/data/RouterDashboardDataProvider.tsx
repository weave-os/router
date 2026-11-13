"use client";

import type { RouterDashboardDataSource } from "./RouterDashboardDataSource";

import { createContext, useContext, useEffect, useState } from "react";

import { LoadState } from "../tools/LoadState";

const RouterDashboardDataContext = createContext<RouterDashboardDataSource | null>(null);

export interface RouterDashboardDataProviderProps extends React.PropsWithChildren {
  dataSource: RouterDashboardDataSource;
}

export function RouterDashboardDataProvider({
  children,
  dataSource,
}: RouterDashboardDataProviderProps) {
  return <RouterDashboardDataContext value={dataSource}>{children}</RouterDashboardDataContext>;
}

export function useRouterDashboardDataSource(): RouterDashboardDataSource {
  const dataSource = useContext(RouterDashboardDataContext);
  if (dataSource == null) {
    throw new Error("useRouterDashboardDataSource must be used inside RouterDashboardDataProvider");
  }
  return dataSource;
}

/**
 * Runs `load` against the host's data source whenever `deps` change. Previous data stays visible
 * as `reloading` while a new range loads; responses from superseded calls are dropped.
 */
export function useRouterDashboardQuery<T>(
  load: ((dataSource: RouterDashboardDataSource) => Promise<T>) | null,
  deps: React.DependencyList,
): LoadState<T> {
  const dataSource = useRouterDashboardDataSource();
  const [state, setState] = useState<LoadState<T>>(() =>
    load == null ? LoadState.notStartedLoading() : LoadState.loading(),
  );

  useEffect(() => {
    if (load == null) return;
    let isCurrent = true;
    setState(LoadState.mapMaybeReloading);
    load(dataSource).then(
      value => {
        if (isCurrent) setState(LoadState.loaded(value));
      },
      (error: unknown) => {
        if (!isCurrent) return;
        setState(LoadState.error(error instanceof Error ? error : new Error(String(error))));
      },
    );
    return () => {
      isCurrent = false;
    };
    // oxlint-disable-next-line react-hooks/exhaustive-deps -- callers own `deps`, like useEffect.
  }, [dataSource, ...deps]);

  return state;
}
