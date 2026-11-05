export enum RouterSubscriptionProvider {
  Claude = "claude",
  Codex = "codex",
}

export enum RouterSubscriptionSeatState {
  Active = "active",
  Cooldown = "cooldown",
  Disabled = "disabled",
  Exhausted = "exhausted",
  ReconnectRequired = "reconnect_required",
  Unknown = "unknown",
}

/** One provider quota window as the seat's newest served turn reported it. */
export interface RouterSubscriptionLimitWindow {
  /** ISO timestamp; null when the provider omitted it. */
  resetsAt: string | null;
  usedFraction: number;
  windowMinutes: number;
}

/** One Claude/Codex subscription in the router pool with its current quota reading. */
export interface RouterSubscriptionPoolSeat {
  cooldownUntil: string | null;
  displayName: string | null;
  enabled: boolean;
  id: string;
  /** Empty when the seat served no turn within the reading lookback. */
  limitWindows: readonly RouterSubscriptionLimitWindow[];
  limitsObservedAt: string | null;
  ownerName: string | null;
  provider: RouterSubscriptionProvider;
  /** Successful upstream requests over the last 7 days. */
  servedRequests: number;
  state: RouterSubscriptionSeatState;
}

export interface RouterSubscriptionPool {
  /** False when the router runs without subscription pools; seats is then empty. */
  enabled: boolean;
  seats: readonly RouterSubscriptionPoolSeat[];
}

export interface SubscriptionsDataSource {
  pool(): Promise<RouterSubscriptionPool>;
}

/** A host rejects a read the signed-in person may not make; pages explain instead of erroring. */
export class RouterForbiddenError extends Error {
  constructor(message = "Forbidden") {
    super(message);
    this.name = "RouterForbiddenError";
  }
}
