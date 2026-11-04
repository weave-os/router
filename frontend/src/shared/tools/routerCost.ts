/** Savings as a percentage of comparison cost; undefined without a priced baseline. */
export function routerSavingsRate(actual: number, comparison: number | null): number | null {
  return comparison == null || comparison === 0 ? null : ((comparison - actual) / comparison) * 100;
}
