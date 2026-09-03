/**
 * Content-Length framing and coordinate conversion.
 *
 * Both transports in this subsystem speak the same wire format: LSP servers
 * over stdio, and the subagent broker over a local socket. Framing lives here
 * so neither owns it.
 */

import { fileURLToPath, pathToFileURL } from "node:url";

export interface JsonRpcMessage {
	jsonrpc?: string;
	id?: number | string | null;
	method?: string;
	params?: unknown;
	result?: unknown;
	error?: { code: number; message: string; data?: unknown };
}

const HEADER_SEPARATOR = "\r\n\r\n";

/** Buffered-bytes ceiling. A peer that blows through it is malfunctioning; the owner kills it. */
export const MAX_FRAME_BYTES = 16 * 1024 * 1024;

export function encodeFrame(message: unknown): Buffer {
	const body = Buffer.from(JSON.stringify(message), "utf8");
	// Content-Length counts BYTES, not characters — a UTF-8 body with any
	// non-ASCII content would otherwise desynchronize the peer's parser.
	return Buffer.concat([Buffer.from(`Content-Length: ${body.length}${HEADER_SEPARATOR}`, "ascii"), body]);
}

export interface FrameParser {
	push(chunk: Buffer): void;
}

/**
 * Incremental parser: handles several frames per chunk, one frame split across
 * chunks, and extra headers (`Content-Type`). A body that is not JSON costs
 * that one message; `onOverflow` fires once and the parser then stays quiet so
 * the owner can tear the peer down.
 */
export function createFrameParser(onMessage: (message: JsonRpcMessage) => void, onOverflow?: (error: Error) => void): FrameParser {
	let buffer: Buffer = Buffer.alloc(0);
	let overflowed = false;

	return {
		push(chunk: Buffer): void {
			if (overflowed) return;
			buffer = buffer.length === 0 ? chunk : Buffer.concat([buffer, chunk]);
			if (buffer.length > MAX_FRAME_BYTES) {
				overflowed = true;
				buffer = Buffer.alloc(0);
				onOverflow?.(new Error(`framed message exceeded ${MAX_FRAME_BYTES} bytes`));
				return;
			}

			while (true) {
				const headerEnd = buffer.indexOf(HEADER_SEPARATOR);
				if (headerEnd === -1) return;
				const header = buffer.subarray(0, headerEnd).toString("ascii");
				const match = /content-length:\s*(\d+)/i.exec(header);
				if (!match) {
					// No length means no way to find the next boundary; drop the header
					// block and resynchronize on whatever follows it.
					buffer = buffer.subarray(headerEnd + HEADER_SEPARATOR.length);
					continue;
				}
				const bodyStart = headerEnd + HEADER_SEPARATOR.length;
				const bodyLength = Number(match[1]);
				if (buffer.length < bodyStart + bodyLength) return;
				const body = buffer.subarray(bodyStart, bodyStart + bodyLength).toString("utf8");
				buffer = buffer.subarray(bodyStart + bodyLength);
				try {
					onMessage(JSON.parse(body) as JsonRpcMessage);
				} catch {
					/* malformed body — skip this message, keep the stream */
				}
			}
		},
	};
}

export function pathToUri(filePath: string): string {
	return pathToFileURL(filePath).toString();
}

export function uriToPath(uri: string): string {
	if (!uri.startsWith("file:")) return uri;
	try {
		return fileURLToPath(uri);
	} catch {
		// Some servers emit non-canonical file URIs (unencoded spaces).
		return decodeURIComponent(uri.replace(/^file:\/\//, ""));
	}
}

/**
 * The operation vocabulary. It lives beside the framing rather than with the pi
 * tool because it is literally the broker's wire payload — keeping it here lets
 * the broker stay below the tool adapter instead of importing back into it.
 */
export const LSP_OPERATIONS = ["definition", "references", "hover", "documentSymbol", "diagnostics"] as const;

export type LspOperation = (typeof LSP_OPERATIONS)[number];

/** Operations that resolve a point in a file, and so require line + column. */
export const POSITION_OPERATIONS = new Set<LspOperation>(["definition", "references", "hover"]);

export interface LspOperationParams {
	operation: LspOperation;
	path: string;
	line?: number;
	column?: number;
}

export interface LspPosition {
	line: number;
	character: number;
}

/** Model-facing 1-based line/column to the 0-based position LSP uses. */
export function toLspPosition(line: number, column: number): LspPosition {
	return { line: Math.max(0, Math.trunc(line) - 1), character: Math.max(0, Math.trunc(column) - 1) };
}

export function fromLspPosition(position: LspPosition | undefined): { line: number; column: number } {
	return { line: (position?.line ?? 0) + 1, column: (position?.character ?? 0) + 1 };
}
