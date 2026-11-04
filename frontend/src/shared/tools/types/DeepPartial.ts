export type DeepPartial<T> =
  // oxlint-disable-next-line typescript/no-unsafe-function-type
  T extends Function
    ? T
    : T extends Array<infer U>
      ? _DeepPartialArray<U>
      : T extends object
        ? _DeepPartialObject<T>
        : T | undefined;

type _DeepPartialArray<T> = Array<DeepPartial<T>>;
type _DeepPartialObject<T> = { [P in keyof T]?: DeepPartial<T[P]> };
