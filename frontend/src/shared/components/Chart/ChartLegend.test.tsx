import { render, screen } from "@testing-library/react";

import { getChartContext } from "./ChartContext";
import { ChartLegend } from "./ChartLegend";
import { ChartSeriesType } from "./types";

const ChartContext = getChartContext<string, number, number>();

const PAYLOAD = [
  { color: "#111111", type: "square" as const, value: "team-a" },
  { color: "#222222", type: "square" as const, value: "team-b" },
];

interface RenderOptions {
  hiddenSeries?: ReadonlySet<string>;
  hideHiddenSeriesFromLegend?: boolean;
}

function renderLegend({
  hiddenSeries = new Set(),
  hideHiddenSeriesFromLegend,
}: RenderOptions = {}) {
  render(
    <ChartContext.Provider
      value={{
        activeSeries: undefined,
        config: undefined,
        hiddenSeries,
        hideHiddenSeriesFromLegend,
        setActiveSeries: () => {},
        toggleSeriesVisibility: () => {},
      }}
    >
      <ChartLegend payload={PAYLOAD} />
    </ChartContext.Provider>,
  );
}

test("renders a hidden series dimmed by default (not removed)", () => {
  renderLegend({ hiddenSeries: new Set(["team-b"]) });

  expect(screen.getByText("team-a")).toBeInTheDocument();
  expect(screen.getByText("team-b")).toBeInTheDocument();
});

test("removes a hidden series from the legend when hideHiddenSeriesFromLegend is set", () => {
  renderLegend({ hiddenSeries: new Set(["team-b"]), hideHiddenSeriesFromLegend: true });

  expect(screen.getByText("team-a")).toBeInTheDocument();
  expect(screen.queryByText("team-b")).not.toBeInTheDocument();
});

test("hatches the swatch of a dashed bar series and leaves other swatches solid", () => {
  render(
    <ChartContext.Provider
      value={{
        activeSeries: undefined,
        config: { "team-a": {}, "team-b": { dashed: true } },
        hiddenSeries: new Set(),
        setActiveSeries: () => {},
        toggleSeriesVisibility: () => {},
      }}
    >
      <ChartLegend payload={PAYLOAD} />
    </ChartContext.Provider>,
  );

  const swatchFor = (label: string) => screen.getByText(label).parentElement!.querySelector("div")!;
  expect(swatchFor("team-a").className).not.toContain("repeating-linear-gradient");
  expect(swatchFor("team-b").className).toContain("repeating-linear-gradient");
});

test("renders line series as solid or dashed lines in the legend", () => {
  render(
    <ChartContext.Provider
      value={{
        activeSeries: undefined,
        config: {
          "team-a": { type: ChartSeriesType.Line },
          "team-b": { dashed: true, type: ChartSeriesType.Line },
        },
        hiddenSeries: new Set(),
        setActiveSeries: () => {},
        toggleSeriesVisibility: () => {},
      }}
    >
      <ChartLegend payload={PAYLOAD} />
    </ChartContext.Provider>,
  );

  const lineFor = (label: string) =>
    screen.getByText(label).parentElement!.querySelector("svg line")!;
  expect(lineFor("team-a").getAttribute("stroke")).toBe("#111111");
  expect(lineFor("team-a").getAttribute("stroke-dasharray")).toBeNull();
  expect(lineFor("team-b").getAttribute("stroke")).toBe("#222222");
  expect(lineFor("team-b").getAttribute("stroke-dasharray")).toBe("0.1 4");
  expect(lineFor("team-b").getAttribute("stroke-linecap")).toBe("square");
});

test("keeps every series visible when none are hidden, even with hideHiddenSeriesFromLegend set", () => {
  renderLegend({ hideHiddenSeriesFromLegend: true });

  expect(screen.getByText("team-a")).toBeInTheDocument();
  expect(screen.getByText("team-b")).toBeInTheDocument();
});
