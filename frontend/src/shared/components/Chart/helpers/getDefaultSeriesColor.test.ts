import { hashString } from "../../../tools/hash";
import { hashStringToIndex } from "./getDefaultSeriesColor";

test.each([
  ["", 2166136261],
  ["a", 3826002220],
  ["hello", 1335831723],
])("the shared hash preserves FNV-1a for %s", (value, expected) => {
  expect(hashString(value) >>> 0).toBe(expected);
});

test.each(["", "account-a", "project-123", "é", "🧶"])(
  "palette slots remain compatible for %s",
  value => {
    let legacyHash = 2166136261;
    for (let index = 0; index < value.length; index++) {
      legacyHash ^= value.charCodeAt(index);
      legacyHash = Math.imul(legacyHash, 16777619);
    }
    for (const modulus of [5, 10, 17, 32])
      expect(hashStringToIndex(value, modulus)).toBe(Math.abs(legacyHash) % modulus);
  },
);
