export type BrandedString<Name extends string> = string & { __brand: Name };
