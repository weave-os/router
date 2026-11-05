import type React from "react";

import { useCallback, useEffect, useRef } from "react";

/** Identifies a keyboard-focusable mark (bar, dot) across Recharts remounts. */
export const CHART_MARK_ATTRIBUTE = "data-chart-mark";

export function getChartMarkId(seriesKey: unknown, index: number | undefined): string {
  return `${String(seriesKey)}:${index ?? ""}`;
}

/**
 * Recharts 3 keys each bar/area series' marks by an id it regenerates on every re-render, so any
 * chart re-render (such as the series highlight set when a mark gains or loses focus) replaces every
 * mark element. Focus falls back to <body>, and the next Tab lands on the first mark again, trapping
 * keyboard users in the chart. This remembers the keyboard-focused mark and refocuses its
 * replacement.
 */
export function useRestoreChartMarkFocus(containerRef: React.RefObject<HTMLElement | null>) {
  const focusedMarkIdRef = useRef<string | null>(null);

  const forgetFocusedMark = useCallback(() => {
    focusedMarkIdRef.current = null;
  }, []);

  // Recharts can remount marks from its own store without re-rendering this component, so watch the
  // DOM. Restoring again a frame later covers a remount mid focus change (from a blur), where the
  // browser drops the restored focus when it finishes moving focus to the removed element.
  useEffect(() => {
    const container = containerRef.current;
    if (container == null) return;
    let frame = 0;
    const restoreFocusedMark = () => {
      const markId = focusedMarkIdRef.current;
      if (markId == null || document.activeElement !== document.body) return;
      container
        .querySelector<SVGElement>(`[${CHART_MARK_ATTRIBUTE}="${CSS.escape(markId)}"][tabindex]`)
        ?.focus({ preventScroll: true });
    };
    const observer = new MutationObserver(() => {
      if (focusedMarkIdRef.current == null) return;
      restoreFocusedMark();
      cancelAnimationFrame(frame);
      frame = requestAnimationFrame(restoreFocusedMark);
    });
    // Pressing a mark would move focus to it, and that focus change remounts the pressed mark before
    // the mouseup, so its click never fires. Keep focus where it is instead.
    const keepFocusOnMarkPress = (event: MouseEvent) => {
      if (
        event.target instanceof Element &&
        event.target.closest(`[${CHART_MARK_ATTRIBUTE}]`) != null
      ) {
        event.preventDefault();
      }
    };
    // A Tab queued behind a slow re-render can run before the restore, so restore first.
    const restoreBeforeTab = (event: KeyboardEvent) => {
      if (event.key === "Tab") restoreFocusedMark();
    };
    observer.observe(container, { childList: true, subtree: true });
    container.addEventListener("mousedown", keepFocusOnMarkPress);
    document.addEventListener("keydown", restoreBeforeTab, { capture: true });
    return () => {
      observer.disconnect();
      container.removeEventListener("mousedown", keepFocusOnMarkPress);
      cancelAnimationFrame(frame);
      document.removeEventListener("keydown", restoreBeforeTab, { capture: true });
      document.removeEventListener("pointerdown", forgetFocusedMark, { capture: true });
    };
  }, [containerRef, forgetFocusedMark]);

  const handleFocus = useCallback(
    (event: React.FocusEvent) => {
      const markId = event.target.getAttribute(CHART_MARK_ATTRIBUTE);
      const isKeyboardFocus =
        event.target.matches(":focus-visible") || markId === focusedMarkIdRef.current;
      if (markId == null || !isKeyboardFocus) return;
      focusedMarkIdRef.current = markId;
      // A pointer press moves focus on purpose, even when the mark it leaves is then remounted.
      document.addEventListener("pointerdown", forgetFocusedMark, { capture: true, once: true });
    },
    [forgetFocusedMark],
  );

  // Tabbing between marks re-renders the chart from the blur, removing the next mark before it gets
  // focus, so remember the destination. A blur with no destination is the mark itself being removed.
  const handleBlur = useCallback(
    (event: React.FocusEvent) => {
      const nextElement = event.relatedTarget;
      if (nextElement == null) return;
      const isKeyboardMove =
        event.target.getAttribute(CHART_MARK_ATTRIBUTE) === focusedMarkIdRef.current &&
        containerRef.current?.contains(nextElement) === true;
      focusedMarkIdRef.current = isKeyboardMove
        ? nextElement.getAttribute(CHART_MARK_ATTRIBUTE)
        : null;
    },
    [containerRef],
  );

  return { handleBlur, handleFocus };
}
