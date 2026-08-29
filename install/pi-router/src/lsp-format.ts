/**
 * LSP result shapes to the text the model reads.
 *
 * Pure: the only filesystem touch (a source line for grep-style context) is an
 * injected reader. Empty results are friendly text rather than errors, matching
 * pi's own grep tool ("No matches found") — a model that reads "not found"
 * moves on, a model that reads an error retries.
 */

import * as fs from "node:fs";
import * as path from "node:path";
import { fromLspPosition, uriToPath, type LspPosition } from "./lsp-protocol.js";

const MAX_LINE_CHARS = 500;
const MAX_HOVER_CHARS = 8000;
const MAX_SYMBOLS = 200;
const MAX_DIAGNOSTICS = 50;

export interface LspRange {
	start: LspPosition;
	end: LspPosition;
}

export interface LspLocation {
	uri: string;
	range: LspRange;
}

export interface LspLocationLink {
	targetUri: string;
	targetRange: LspRange;
	targetSelectionRange?: LspRange;
}

export interface NormalizedLocation {
	path: string;
	line: number;
	column: number;
}

export interface LspDiagnostic {
	range: LspRange;
	severity?: number;
	code?: string | number;
	source?: string;
	message: string;
}

interface LspDocumentSymbol {
	name: string;
	kind: number;
	detail?: string;
	range?: LspRange;
	selectionRange?: LspRange;
	children?: LspDocumentSymbol[];
}

interface LspSymbolInformation {
	name: string;
	kind: number;
	containerName?: string;
	location: LspLocation;
}

/** LSP 3.17 SymbolKind, 1-based. */
const SYMBOL_KINDS = [
	"File", "Module", "Namespace", "Package", "Class", "Method", "Property", "Field", "Constructor",
	"Enum", "Interface", "Function", "Variable", "Constant", "String", "Number", "Boolean", "Array",
	"Object", "Key", "Null", "EnumMember", "Struct", "Event", "Operator", "TypeParameter",
];

const SEVERITY_NAMES = ["error", "warning", "info", "hint"];

export type LineReader = (filePath: string, line: number) => string | undefined;

/** Memoized per call: a references result routinely hits the same file dozens of times. */
export function createLineReader(readFile: (p: string) => string = (p) => fs.readFileSync(p, "utf8")): LineReader {
	const cache = new Map<string, string[] | undefined>();
	return (filePath, line) => {
		if (!cache.has(filePath)) {
			try {
				cache.set(filePath, readFile(filePath).split(/\r?\n/));
			} catch {
				cache.set(filePath, undefined);
			}
		}
		return cache.get(filePath)?.[line - 1];
	};
}

export function displayPath(target: string, cwd: string): string {
	const relative = path.relative(cwd, target);
	if (!relative || relative.startsWith("..") || path.isAbsolute(relative)) return target;
	return relative;
}

function truncateLine(text: string): string {
	const trimmed = text.trim();
	return trimmed.length > MAX_LINE_CHARS ? `${trimmed.slice(0, MAX_LINE_CHARS)}... [truncated]` : trimmed;
}

function isRecord(value: unknown): value is Record<string, unknown> {
	return typeof value === "object" && value !== null;
}

/** `Location | Location[] | LocationLink[] | null` collapsed to one 1-based shape. */
export function normalizeLocations(result: unknown): NormalizedLocation[] {
	if (result === null || result === undefined) return [];
	const items = Array.isArray(result) ? result : [result];
	const out: NormalizedLocation[] = [];
	for (const item of items) {
		if (!isRecord(item)) continue;
		// LocationLink points at targetSelectionRange (the name) rather than
		// targetRange (the whole declaration body), which is what a reader wants.
		const uri = typeof item.uri === "string" ? item.uri : typeof item.targetUri === "string" ? item.targetUri : undefined;
		if (!uri) continue;
		const range = (item.range ?? item.targetSelectionRange ?? item.targetRange) as LspRange | undefined;
		const { line, column } = fromLspPosition(range?.start);
		out.push({ path: uriToPath(uri), line, column });
	}
	return out;
}

export interface LocationListOptions {
	cwd: string;
	readLine: LineReader;
	limit: number;
	/** Plural noun for the header, e.g. "references". Omit for a bare list. */
	label?: string;
}

export function formatLocations(locations: NormalizedLocation[], options: LocationListOptions): string {
	const shown = locations.slice(0, Math.max(1, options.limit));
	const lines = shown.map((location) => {
		const source = options.readLine(location.path, location.line);
		const suffix = source === undefined ? "" : ` ${truncateLine(source)}`;
		return `${displayPath(location.path, options.cwd)}:${location.line}:${location.column}:${suffix}`;
	});
	if (!options.label) return lines.join("\n");
	const header =
		locations.length > shown.length
			? `${locations.length} ${options.label} (showing first ${shown.length})`
			: `${locations.length} ${options.label}`;
	return [header, ...lines].join("\n");
}

export function formatHover(result: unknown): string {
	if (!isRecord(result)) return "";
	const rendered = renderHoverContents(result.contents).trim();
	return rendered.length > MAX_HOVER_CHARS ? `${rendered.slice(0, MAX_HOVER_CHARS)}\n... [truncated]` : rendered;
}

function renderHoverContents(contents: unknown): string {
	if (typeof contents === "string") return contents;
	if (Array.isArray(contents)) return contents.map(renderHoverContents).filter(Boolean).join("\n\n");
	if (!isRecord(contents)) return "";
	// MarkupContent
	if (typeof contents.value === "string" && typeof contents.kind === "string") return contents.value;
	// MarkedString: {language, value}
	if (typeof contents.value === "string") {
		const language = typeof contents.language === "string" ? contents.language : "";
		return `\`\`\`${language}\n${contents.value}\n\`\`\``;
	}
	return "";
}

function symbolKindName(kind: number): string {
	return SYMBOL_KINDS[kind - 1] ?? "Symbol";
}

export function formatSymbols(result: unknown, cwd: string): string {
	if (!Array.isArray(result) || result.length === 0) return "";
	const lines: string[] = [];
	let remaining = MAX_SYMBOLS;

	const walk = (symbols: LspDocumentSymbol[], depth: number): void => {
		for (const symbol of symbols) {
			if (remaining <= 0) return;
			remaining -= 1;
			const { line } = fromLspPosition((symbol.selectionRange ?? symbol.range)?.start);
			const detail = symbol.detail ? ` ${symbol.detail}` : "";
			lines.push(`${"  ".repeat(depth)}${symbolKindName(symbol.kind)} ${symbol.name}${detail} :${line}`);
			if (symbol.children?.length) walk(symbol.children, depth + 1);
		}
	};

	const first = result[0] as Record<string, unknown>;
	if (isRecord(first) && isRecord(first.location)) {
		// Flat SymbolInformation[] — older servers, no hierarchy available.
		for (const symbol of (result as LspSymbolInformation[]).slice(0, MAX_SYMBOLS)) {
			const { line } = fromLspPosition(symbol.location?.range?.start);
			const container = symbol.containerName ? ` (in ${symbol.containerName})` : "";
			const file = displayPath(uriToPath(symbol.location?.uri ?? ""), cwd);
			lines.push(`${symbolKindName(symbol.kind)} ${symbol.name}${container} ${file}:${line}`);
		}
	} else {
		walk(result as LspDocumentSymbol[], 0);
	}

	const total = countSymbols(result as LspDocumentSymbol[]);
	if (total > lines.length) lines.push(`... ${total - lines.length} more symbols`);
	return lines.join("\n");
}

function countSymbols(symbols: LspDocumentSymbol[]): number {
	let total = 0;
	for (const symbol of symbols) {
		total += 1 + (symbol.children ? countSymbols(symbol.children) : 0);
	}
	return total;
}

export function formatDiagnostics(diagnostics: LspDiagnostic[], filePath: string, cwd: string): string {
	if (diagnostics.length === 0) return "";
	const counts = [0, 0, 0, 0];
	for (const diagnostic of diagnostics) counts[(diagnostic.severity ?? 1) - 1] += 1;
	const summary = SEVERITY_NAMES.map((name, index) => (counts[index] > 0 ? `${counts[index]} ${name}${counts[index] === 1 ? "" : "s"}` : null))
		.filter(Boolean)
		.join(", ");

	const file = displayPath(filePath, cwd);
	const lines = diagnostics.slice(0, MAX_DIAGNOSTICS).map((diagnostic) => {
		const { line, column } = fromLspPosition(diagnostic.range?.start);
		const severity = SEVERITY_NAMES[(diagnostic.severity ?? 1) - 1] ?? "error";
		const source = diagnostic.source ? ` [${diagnostic.source}]` : "";
		return `${severity} ${file}:${line}:${column}: ${diagnostic.message.replace(/\s*\n\s*/g, " ")}${source}`;
	});
	if (diagnostics.length > lines.length) lines.push(`... ${diagnostics.length - lines.length} more`);
	return [summary, ...lines].join("\n");
}
