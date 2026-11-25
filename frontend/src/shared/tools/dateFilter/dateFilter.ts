import { DateScalar } from "../../objects/DateScalar";

/** A day range; a missing `end` stays open past today, and `end` itself is excluded. */
export interface DateFilter {
  end?: DateScalar;
  start?: DateScalar;
}

export const Filter = {
  date: (start?: DateScalar, end?: DateScalar): DateFilter => ({ end, start }),
};
