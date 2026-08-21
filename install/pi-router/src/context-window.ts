/**
 * Router-served context-window parsing shared by the model registry and
 * compaction guard. The response header is the source of truth for the model
 * that actually served a routed request.
 */

/** Parse a router context-window header without accepting fractional or unsafe values. */
export function parseRoutedContextWindow(value: string | undefined): number | undefined {
	if (!value || !/^[1-9]\d*$/.test(value)) return undefined;
	const contextWindow = Number(value);
	return Number.isSafeInteger(contextWindow) ? contextWindow : undefined;
}
