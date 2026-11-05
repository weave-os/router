/** Savings as a percentage of comparison cost; null without a priced baseline (missing or zero comparison). */
export function routerSavingsRate(actual: number, comparison: number | null): number | null {
  return comparison == null || comparison === 0 ? null : ((comparison - actual) / comparison) * 100;
}
