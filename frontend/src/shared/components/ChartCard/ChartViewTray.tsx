"use client";

import { createContext, useContext, useLayoutEffect, useRef, useState } from "react";
import { createPortal } from "react-dom";

/**
 * Width a tray leaves for the title line: the title with its help and source icons, the
 * chart menu, and the gaps around them.
 */
const TITLE_LINE_RESERVE_PX = 360;

export interface ChartHeaderLayout {
  /** Width of the card's header, which the title line and the tray share. */
  availableWidth: number;
  /** The slot in the card's top-right cluster, on the title's line. */
  titleLineSlot: HTMLElement | null;
}

export const ChartHeaderLayoutContext = createContext<ChartHeaderLayout>({
  availableWidth: 0,
  titleLineSlot: null,
});

export interface ChartViewTrayProps {
  children: React.ReactNode;
}

/**
 * Places a chart's view tray on the title's line when the card is wide enough to hold it, and on
 * its own row under the headline otherwise. Render it inside `ChartCard`'s `headerBottom`; outside
 * a `ChartCard` it always renders inline.
 */
export function ChartViewTray({ children }: ChartViewTrayProps) {
  const { availableWidth, titleLineSlot } = useContext(ChartHeaderLayoutContext);
  const measureRef = useRef<HTMLDivElement>(null);
  const [trayWidth, setTrayWidth] = useState<number | null>(null);

  // Measured at its one-line width, since under the headline the tray may wrap.
  useLayoutEffect(() => {
    const trayWrapper = measureRef.current;
    if (trayWrapper == null) return;
    trayWrapper.style.width = "max-content";
    setTrayWidth(trayWrapper.offsetWidth);
    trayWrapper.style.width = "";
  }, [children]);

  const tray = (
    <div ref={measureRef} className="max-w-full">
      {children}
    </div>
  );

  const fitsOnTitleLine =
    titleLineSlot != null &&
    trayWidth != null &&
    availableWidth >= trayWidth + TITLE_LINE_RESERVE_PX;

  return fitsOnTitleLine ? createPortal(tray, titleLineSlot) : tray;
}
