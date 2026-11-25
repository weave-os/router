import { format, parseISO } from "date-fns";

import { DateFormat } from "../tools/time";
import { BrandedString } from "../tools/types";

/** A calendar day as `yyyy-MM-dd`, read in the viewer's timezone. */
export type DateScalar = BrandedString<"DateScalar">;

export const DateScalar = {
  fromDate: (date: Date | number): DateScalar =>
    format(date, DateFormat.YearMonthDayFull) as DateScalar,

  toDate: (date: DateScalar): Date => parseISO(date),

  today: (): DateScalar => DateScalar.fromDate(new Date()),
};
