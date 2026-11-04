import { useIsMounted } from "@/shared/hooks/useIsMounted";
import { LoadState } from "@/shared/tools/LoadState";
import { unreachable } from "@/shared/tools/types";

import { useEffect, useState } from "react";

export interface LoadStateWrapperProps<T> {
  /**
   * The load state whose value should be rendered.
   */
  state: LoadState<T>;

  /**
   * If provided, delays showing the loading state by this many milliseconds.
   *
   * @default 400
   */
  loadingDelayMs?: number;

  /**
   * A render function that receives the value of the load state and returns the component to
   * render, if the load state is ready.
   */
  children: (value: T) => React.ReactNode;

  /**
   * If provided, this component will be rendered while the load state is loading (and
   * {@link loadingDelayMs} has passed).
   */
  loading?: React.ReactNode;

  /**
   * If provided, this component will be rendered if the load state is in an error state.
   */
  error?: (error: Error) => React.ReactNode;

  /**
   * If true, the error will not be shown if the load state has data.
   *
   * @default false
   */
  hideErrorWithData?: boolean;
}

/**
 * Renders the given {@link LoadState} value, or a loading/error state if the value is not ready.
 */
export function LoadStateWrapper<T>({
  children,
  error,
  hideErrorWithData = false,
  loading,
  loadingDelayMs = 400,
  state,
}: LoadStateWrapperProps<T>) {
  /** Whether to show the loading state. May lag behind whether or not the data is actually loading. */
  const [showLoadingState, setShowLoadingState] = useState(loadingDelayMs === 0);
  const isMounted = useIsMounted();

  const isLoading = LoadState.isLoading(state) || LoadState.isNotStartedLoading(state);

  useEffect(() => {
    if (!isLoading) {
      // oxlint-disable-next-line react/set-state-in-effect
      setShowLoadingState(false);
      return;
    }

    const timer = setTimeout(() => {
      if (isMounted.current) {
        setShowLoadingState(true);
      }
    }, loadingDelayMs);

    return () => {
      clearTimeout(timer);
    };
  }, [isLoading, isMounted, loadingDelayMs]);

  const showLoading = (loadingDelayMs === 0 && isLoading) || showLoadingState;

  if (showLoading) return <>{loading ?? null}</>;
  if (isLoading) return null;

  // If there's an error state but we also have data to show and the caller has opted in, show the
  // data instead of the error.
  if (LoadState.isError(state) && hideErrorWithData && state.previousValue != null)
    return <>{children(state.previousValue)}</>;
  if (LoadState.isError(state)) return <>{error?.(state.error) ?? null}</>;
  if (LoadState.isReady(state)) return <>{children(state.value)}</>;

  unreachable(state);
}
