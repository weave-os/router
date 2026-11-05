export type BrandedNumber<Name extends string> = number & { __brand: Name };
