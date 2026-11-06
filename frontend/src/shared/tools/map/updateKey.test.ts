import { updateKey } from "./updateKey";

describe("updateKey", () => {
  test("updates an existing key", () => {
    const map = new Map([["a", 1]]);
    const result = updateKey(map, "a", x => (x ?? 0) + 1);
    expect(map.get("a")).toBe(2); // checking the original map is mutated
    expect(result.get("a")).toBe(2);
  });

  test("adds a new key", () => {
    const map = new Map<string, number>();
    const result = updateKey(map, "a", x => (x ?? 0) + 1);
    expect(map.get("a")).toBe(1); // checking the original map is mutated
    expect(result.get("a")).toBe(1);
  });
});
