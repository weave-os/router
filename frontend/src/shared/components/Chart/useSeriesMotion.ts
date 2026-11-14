import React, { useEffect, useReducer } from "react";

import {
  MOTION_DURATION_MS,
  MOTION_STAGGER_STEP_MS,
  usePrefersReducedMotion,
} from "../../design-system/motion";
import { LoadState } from "../../tools/LoadState";

export enum SeriesMotionPhase {
  /** No series are mounted; the next reveal mounts them. */
  Hidden = "hidden",
  /** Bars rise one data point after another while lines draw in. */
  Entering = "entering",
  Shown = "shown",
  /** The last loaded rows stay plotted while bars lower one after another and lines erase. */
  Exiting = "exiting",
}

export const SERIES_ENTER_DURATION_MS = MOTION_DURATION_MS.default;
export const SERIES_EXIT_DURATION_MS = MOTION_DURATION_MS.panel;
// Caps the domino on dense charts, where the full step per data point would drag on for seconds.
const SERIES_MAX_STAGGER_SPAN_MS = MOTION_DURATION_MS.expressive;

/** Caps the step so `count` points' stagger never exceeds SERIES_MAX_STAGGER_SPAN_MS. */
export function getSeriesStaggerStepMs(count: number) {
  if (count <= 1) return 0;
  return Math.min(MOTION_STAGGER_STEP_MS, SERIES_MAX_STAGGER_SPAN_MS / (count - 1));
}

enum SeriesMotionActionType {
  EntranceFinished = "entranceFinished",
  ExitFinished = "exitFinished",
  Reveal = "reveal",
  RowsChanged = "rowsChanged",
}

type SeriesMotionAction<TRow> =
  | { type: SeriesMotionActionType.EntranceFinished }
  | { type: SeriesMotionActionType.ExitFinished }
  | { now: number; prefersReducedMotion: boolean; type: SeriesMotionActionType.Reveal }
  | {
      now: number;
      prefersReducedMotion: boolean;
      rows: LoadState<TRow[]>;
      type: SeriesMotionActionType.RowsChanged;
    };

interface SeriesMotionState<TRow> {
  exitingRows: TRow[];
  /** Whether Recharts should animate in-place updates, such as toggling a series in the legend. */
  isAnimationActive: boolean;
  /** Set by an exit so the stale rows of a reload are not revealed again before fresh rows load. */
  isAwaitingFreshRows: boolean;
  phase: SeriesMotionPhase;
  /** `performance.now()` when the current enter/exit began, so remounted bars skip ahead. */
  phaseStartedAt: number;
  rows: LoadState<TRow[]>;
}

function reduceSeriesMotion<TRow>(
  state: SeriesMotionState<TRow>,
  action: SeriesMotionAction<TRow>,
): SeriesMotionState<TRow> {
  switch (action.type) {
    case SeriesMotionActionType.RowsChanged: {
      const previousRows = state.rows;
      const { rows } = action;
      // Turning on reduced motion mid-exit stops the exit and falls through to the reduced-motion
      // state below: the stale chart under the loading overlay.
      const isPlotted =
        state.phase === SeriesMotionPhase.Shown ||
        state.phase === SeriesMotionPhase.Entering ||
        (state.phase === SeriesMotionPhase.Exiting && action.prefersReducedMotion);
      if (!isPlotted) return { ...state, rows };

      const isLeavingLoadedRows =
        LoadState.isLoaded(previousRows) &&
        !LoadState.isLoaded(rows) &&
        previousRows.value.length > 0;
      // Reduced motion keeps the stale chart under the loading overlay instead of playing the exit.
      if (isLeavingLoadedRows && !action.prefersReducedMotion) {
        return {
          ...state,
          exitingRows: previousRows.value,
          isAnimationActive: false,
          phase: SeriesMotionPhase.Exiting,
          phaseStartedAt: action.now,
          rows,
        };
      }

      // A ready-to-ready change (polling) swaps rows in place without animation.
      return LoadState.isReady(rows)
        ? { ...state, isAnimationActive: false, phase: SeriesMotionPhase.Shown, rows }
        : { ...state, isAnimationActive: false, phase: SeriesMotionPhase.Hidden, rows };
    }

    case SeriesMotionActionType.EntranceFinished:
      return { ...state, phase: SeriesMotionPhase.Shown };

    case SeriesMotionActionType.ExitFinished:
      return {
        ...state,
        exitingRows: [],
        isAwaitingFreshRows: true,
        phase: SeriesMotionPhase.Hidden,
      };

    case SeriesMotionActionType.Reveal:
      return {
        ...state,
        isAnimationActive: !action.prefersReducedMotion,
        isAwaitingFreshRows: false,
        phase: action.prefersReducedMotion ? SeriesMotionPhase.Shown : SeriesMotionPhase.Entering,
        phaseStartedAt: action.now,
      };
  }
}

function canReveal<TRow>(state: SeriesMotionState<TRow>) {
  if (state.phase !== SeriesMotionPhase.Hidden) return false;
  if (LoadState.isLoaded(state.rows)) return true;
  return LoadState.isReady(state.rows) && !state.isAwaitingFreshRows;
}

/**
 * Sequences chart series through loading: rows that arrive after a load animate in, and loaded rows
 * that start reloading animate out before the fresh rows animate in. Both directions stagger by
 * data point, so bars topple in order like dominoes. Nothing reveals until `isPlotMounted`, so a
 * chart that loads offscreen still enters once scrolled to.
 */
export function useSeriesMotion<TRow>(rows: LoadState<TRow[]>, isPlotMounted: boolean) {
  const prefersReducedMotion = usePrefersReducedMotion();
  const [state, dispatch] = useReducer(
    reduceSeriesMotion<TRow>,
    rows,
    (initialRows): SeriesMotionState<TRow> => ({
      exitingRows: [],
      isAnimationActive: false,
      isAwaitingFreshRows: false,
      // Rows already cached at mount start hidden so the reveal below plays the entrance, exactly
      // as rows that finish loading after mount do.
      phase:
        LoadState.isReady(initialRows) && prefersReducedMotion
          ? SeriesMotionPhase.Shown
          : SeriesMotionPhase.Hidden,
      phaseStartedAt: performance.now(),
      rows: initialRows,
    }),
  );

  useEffect(() => {
    dispatch({
      now: performance.now(),
      prefersReducedMotion,
      rows,
      type: SeriesMotionActionType.RowsChanged,
    });
  }, [prefersReducedMotion, rows]);

  // Revealing one render after the rows arrive lets Recharts settle the new axis scale before the
  // series mount; animating both at once draws the series against the old scale.
  const shouldReveal = isPlotMounted && canReveal(state);
  useEffect(() => {
    if (shouldReveal)
      dispatch({
        now: performance.now(),
        prefersReducedMotion,
        type: SeriesMotionActionType.Reveal,
      });
  }, [prefersReducedMotion, shouldReveal]);

  const pointCount =
    state.phase === SeriesMotionPhase.Exiting
      ? state.exitingRows.length
      : LoadState.unwrap(state.rows, []).length;
  const staggerStepMs = getSeriesStaggerStepMs(pointCount);
  const staggerSpanMs = staggerStepMs * Math.max(pointCount - 1, 0);

  useEffect(() => {
    if (state.phase === SeriesMotionPhase.Entering) {
      const timeout = setTimeout(
        () => dispatch({ type: SeriesMotionActionType.EntranceFinished }),
        SERIES_ENTER_DURATION_MS + staggerSpanMs,
      );
      return () => clearTimeout(timeout);
    }
    if (state.phase === SeriesMotionPhase.Exiting) {
      const timeout = setTimeout(
        () => dispatch({ type: SeriesMotionActionType.ExitFinished }),
        SERIES_EXIT_DURATION_MS + staggerSpanMs,
      );
      return () => clearTimeout(timeout);
    }
  }, [staggerSpanMs, state.phase]);

  return {
    exitingRows: state.exitingRows,
    isAnimationActive: state.isAnimationActive,
    phase: state.phase,
    phaseStartedAt: state.phaseStartedAt,
    staggerSpanMs,
    staggerStepMs,
  };
}

/**
 * Erases solid lines from their start in step with the exiting bars. Dots, dashed lines (marked
 * with `dashedLineClassName`), and both entrance sweeps are CSS in ChartSeriesMotion.module.css;
 * this needs script only because the dash pattern depends on each path's length.
 *
 * The animation targets the `.recharts-line` layer, not its path: Recharts remounts the path
 * whenever the points change, which the exit's first renders do. The dash properties inherit down
 * to the path as long as Recharts is not animating the line itself.
 */
interface UseLineExitAnimationArgs {
  containerRef: React.RefObject<HTMLElement | null>;
  dashedLineClassName: string;
  phase: SeriesMotionPhase;
  staggerSpanMs: number;
}

export function useLineExitAnimation({
  containerRef,
  dashedLineClassName,
  phase,
  staggerSpanMs,
}: UseLineExitAnimationArgs) {
  useEffect(() => {
    const container = containerRef.current;
    if (phase !== SeriesMotionPhase.Exiting || container == null) return;

    const exitStartedAt = performance.now();
    const animations = new Map<SVGGElement, Animation>();
    const eraseLines = () => {
      for (const layer of container.querySelectorAll<SVGGElement>(".recharts-line")) {
        const curve = layer.querySelector<SVGPathElement>(".recharts-line-curve");
        if (animations.has(layer) || curve == null || layer.classList.contains(dashedLineClassName))
          continue;

        const length = curve.getTotalLength();
        const animation = layer.animate(
          {
            strokeDasharray: [`${length} ${length}`, `${length} ${length}`],
            strokeDashoffset: [0, -length],
          },
          // Linear keeps the erase front level with the dominoes, whose starts are evenly spaced.
          // Holding the end frame keeps the line hidden until the exit unmounts it.
          { duration: SERIES_EXIT_DURATION_MS + staggerSpanMs, easing: "linear", fill: "forwards" },
        );
        // A line that mounts mid-exit joins the erase where the others are instead of popping in.
        animation.currentTime = performance.now() - exitStartedAt;
        animations.set(layer, animation);
      }
    };

    eraseLines();
    const observer = new MutationObserver(eraseLines);
    observer.observe(container, { childList: true, subtree: true });

    return () => {
      observer.disconnect();
      for (const animation of animations.values()) animation.cancel();
    };
  }, [containerRef, dashedLineClassName, phase, staggerSpanMs]);
}
