import { capTooltipSeries, TooltipSeriesEntry } from "./capTooltipSeries";

describe("capTooltipSeries", () => {
  function make(entries: [string, number][]): TooltipSeriesEntry<string>[] {
    return entries.map(([name, sortValue]) => ({ name, sortValue }));
  }

  it("returns all entries in original order when under the cap", () => {
    const entries = make([
      ["A", 50],
      ["B", 20],
      ["C", 30],
    ]);
    const result = capTooltipSeries({ activeSeries: null, entries, maxRows: 10 });
    expect(result.visible.map(e => e.name)).toEqual(["A", "B", "C"]);
    expect(result.hiddenCount).toBe(0);
  });

  it("returns all entries when exactly at the cap", () => {
    const entries = make([
      ["A", 10],
      ["B", 9],
      ["C", 8],
      ["D", 7],
      ["E", 6],
      ["F", 5],
      ["G", 4],
      ["H", 3],
      ["I", 2],
      ["J", 1],
    ]);
    const result = capTooltipSeries({ activeSeries: null, entries, maxRows: 10 });
    expect(result.visible).toHaveLength(10);
    expect(result.hiddenCount).toBe(0);
  });

  it("caps to top-N by value when over the limit with no active series", () => {
    const entries = make([
      ["A", 5],
      ["B", 20],
      ["C", 3],
      ["D", 50],
      ["E", 1],
      ["F", 10],
      ["G", 9],
      ["H", 30],
      ["I", 2],
      ["J", 4],
      ["K", 15],
      ["L", 8],
    ]);
    const result = capTooltipSeries({ activeSeries: null, entries, maxRows: 5 });
    expect(result.visible.map(e => e.name)).toEqual(["D", "H", "B", "K", "F"]);
    expect(result.hiddenCount).toBe(7);
  });

  it("pins active series first when over the limit", () => {
    const entries = make([
      ["A", 5],
      ["B", 20],
      ["C", 3],
      ["D", 50],
      ["E", 1],
      ["F", 10],
      ["G", 9],
      ["H", 30],
      ["I", 2],
      ["J", 4],
      ["K", 15],
      ["L", 8],
    ]);
    const result = capTooltipSeries({ activeSeries: "E", entries, maxRows: 5 });
    // E (active, value=1) first, then top 4 by value: D(50), H(30), B(20), K(15)
    expect(result.visible.map(e => e.name)).toEqual(["E", "D", "H", "B", "K"]);
    expect(result.hiddenCount).toBe(7);
  });

  it("pins active series first when it's already a top-value entry", () => {
    const entries = make([
      ["A", 5],
      ["B", 20],
      ["C", 3],
      ["D", 50],
      ["E", 1],
      ["F", 10],
      ["G", 9],
      ["H", 30],
      ["I", 2],
      ["J", 4],
      ["K", 15],
      ["L", 8],
    ]);
    const result = capTooltipSeries({ activeSeries: "D", entries, maxRows: 5 });
    // D (active, value=50) first, then top 4 by value: H(30), B(20), K(15), F(10)
    expect(result.visible.map(e => e.name)).toEqual(["D", "H", "B", "K", "F"]);
    expect(result.hiddenCount).toBe(7);
  });

  it("sorts null-value entries last", () => {
    const entries: TooltipSeriesEntry<string>[] = [
      { name: "A", sortValue: 5 },
      { name: "B", sortValue: null },
      { name: "C", sortValue: 3 },
      { name: "D", sortValue: undefined },
      { name: "E", sortValue: 10 },
    ];
    const result = capTooltipSeries({ activeSeries: null, entries, maxRows: 3 });
    // Top 3 descending: E(10), A(5), C(3)
    expect(result.visible.map(e => e.name)).toEqual(["E", "A", "C"]);
    expect(result.hiddenCount).toBe(2);
  });

  it("returns empty and all hidden when maxRows is 0", () => {
    const entries = make([
      ["A", 5],
      ["B", 3],
    ]);
    const result = capTooltipSeries({ activeSeries: null, entries, maxRows: 0 });
    expect(result.visible).toHaveLength(0);
    expect(result.hiddenCount).toBe(2);
  });

  it("returns entries as-is when maxRows is 1 and 1 entry", () => {
    const entries = make([["A", 5]]);
    const result = capTooltipSeries({ activeSeries: null, entries, maxRows: 1 });
    expect(result.visible.map(e => e.name)).toEqual(["A"]);
    expect(result.hiddenCount).toBe(0);
  });

  it("returns only active when maxRows is 1 and multiple entries", () => {
    const entries = make([
      ["A", 5],
      ["B", 10],
      ["C", 3],
    ]);
    const result = capTooltipSeries({ activeSeries: "C", entries, maxRows: 1 });
    expect(result.visible.map(e => e.name)).toEqual(["C"]);
    expect(result.hiddenCount).toBe(2);
  });
});
