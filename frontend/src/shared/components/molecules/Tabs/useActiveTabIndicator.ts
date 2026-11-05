"use client";

import { useLayoutEffect, useRef } from "react";

const ACTIVE_TRIGGER_SELECTOR = ':scope > [data-state="active"]';

/**
 * Places the sliding active-tab pill (`TabsMotion.module.css`) over whichever direct child carries
 * `data-state="active"`, re-measuring when the selection, the triggers, or their sizes change.
 */
export function useActiveTabIndicator<T extends HTMLElement>(isEnabled: boolean) {
  // A ref object, not a ref callback: Radix recomposes the list's ref on every render, and
  // re-attaching would recreate the pill mid-slide.
  const trayRef = useRef<T>(null);

  useLayoutEffect(() => {
    const tray = trayRef.current;
    // Without ResizeObserver (jsdom) the active trigger keeps painting its own fill.
    if (tray === null || !isEnabled || typeof ResizeObserver === "undefined") {
      return;
    }

    const placeIndicator = () => {
      const activeTrigger = tray.querySelector<HTMLElement>(ACTIVE_TRIGGER_SELECTOR);
      // A hidden tray measures zero; drop the pill so it reappears in place instead of growing.
      if (activeTrigger === null || activeTrigger.offsetWidth === 0) {
        delete tray.dataset.tabIndicator;
        return;
      }
      tray.style.setProperty("--tab-indicator-x", `${activeTrigger.offsetLeft}px`);
      tray.style.setProperty("--tab-indicator-y", `${activeTrigger.offsetTop}px`);
      tray.style.setProperty("--tab-indicator-width", `${activeTrigger.offsetWidth}px`);
      tray.style.setProperty("--tab-indicator-height", `${activeTrigger.offsetHeight}px`);
      tray.dataset.tabIndicator = "";
    };

    const resizeObserver = new ResizeObserver(placeIndicator);
    const observeSizes = () => {
      resizeObserver.disconnect();
      resizeObserver.observe(tray);
      for (const trigger of tray.children) {
        resizeObserver.observe(trigger);
      }
    };

    const mutationObserver = new MutationObserver(mutations => {
      if (mutations.some(mutation => mutation.type === "childList")) {
        observeSizes();
      }
      placeIndicator();
    });
    mutationObserver.observe(tray, {
      attributeFilter: ["data-state"],
      attributes: true,
      childList: true,
      subtree: true,
    });

    observeSizes();
    placeIndicator();

    return () => {
      mutationObserver.disconnect();
      resizeObserver.disconnect();
      delete tray.dataset.tabIndicator;
    };
  }, [isEnabled]);

  return trayRef;
}
