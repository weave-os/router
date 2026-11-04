/**
 * Why a host cannot serve part of the dashboard contract. Pages render a typed empty state for
 * these instead of calling an endpoint that does not exist.
 */
export enum UnavailableReason {
  /** The host has no backing data for this capability (e.g. self-hosted has no per-user rollup). */
  NotSupportedByHost = "NotSupportedByHost",
  /** The capability exists but the current viewer cannot use it. */
  NotPermitted = "NotPermitted",
}

export interface Unavailable {
  readonly kind: "unavailable";
  readonly reason: UnavailableReason;
  /** Short user-facing explanation shown in the empty state. */
  readonly message: string;
}

/** A section of the data source that a host either implements or declares unavailable. */
export type Capability<T extends object> = T | Unavailable;

export function unavailable(reason: UnavailableReason, message: string): Unavailable {
  return { kind: "unavailable", message, reason };
}

export function isUnavailable<T extends object>(
  capability: Capability<T>,
): capability is Unavailable {
  return "kind" in capability && capability.kind === "unavailable";
}
