import { render } from "@testing-library/react";

import { ChartTick, ChartTickProps } from "./ChartTick";
import { X_AXIS_TICK_MARGIN } from "./constants";

/**
 * Renders a {@link ChartTick} inside an <svg> (a foreignObject must live in the SVG namespace) and
 * returns the container plus the rendered foreignObject.
 */
function renderChartTick(overrides: Partial<ChartTickProps<string>> = {}) {
  const { container } = render(
    <svg>
      <ChartTick<string>
        x={50}
        y={0}
        width={300}
        height={30}
        index={0}
        visibleTicksCount={3}
        payload={{ value: "acme" }}
        renderTick={value => <span data-testid="tick-content">{value}</span>}
        {...overrides}
      />
    </svg>,
  );
  return { container, foreignObject: container.querySelector("foreignObject") };
}

describe("ChartTick", () => {
  test("renders the tick content", () => {
    const { container } = renderChartTick();
    expect(container.querySelector('[data-testid="tick-content"]')).toHaveTextContent("acme");
  });

  test("centers the foreignObject slot on the bar", () => {
    // tickWidth = 300 / 3 = 100; leftOffset = 50 - 100 / 2 = 0.
    const { foreignObject } = renderChartTick({ visibleTicksCount: 3, width: 300, x: 50 });
    expect(foreignObject).toHaveAttribute("width", "100");
    expect(foreignObject).toHaveAttribute("x", "0");
    expect(foreignObject).toHaveAttribute("height", String(30 - X_AXIS_TICK_MARGIN - 1));
    expect(foreignObject).toHaveStyle({ overflow: "visible" });
  });

  /**
   * Regression guard for a Safari-only layout bug: Safari's <foreignObject> does not resolve
   * percentage sizing (Tailwind `h-full` / `w-full`) on its HTML descendants, which collapses
   * flex-based ticks (e.g. the standup avatar + PR streak) so they drift out from under the bar.
   * The fix is to give the foreignObject's direct child a definite pixel box so percentages inside
   * resolve in every browser. If this wrapper is removed, Safari breaks again.
   */
  test("wraps tick content in a direct child with an explicit pixel box (Safari sizing guard)", () => {
    // tickWidth = 300 / 3 = 100; adjustedHeight = 30 - 5 - 1 = 24.
    const { foreignObject } = renderChartTick({ height: 30, visibleTicksCount: 3, width: 300 });
    const wrapper = foreignObject?.firstElementChild as HTMLElement | null;

    expect(wrapper?.tagName.toLowerCase()).toBe("div");
    expect(wrapper).toHaveStyle({ height: "24px", width: "100px" });
  });

  test("renders nothing when the payload is missing", () => {
    const { foreignObject } = renderChartTick({ payload: undefined });
    expect(foreignObject).toBeNull();
  });

  test("renders nothing before Recharts supplies numeric sizing props", () => {
    const { foreignObject } = renderChartTick({ width: "auto" });
    expect(foreignObject).toBeNull();
  });
});
