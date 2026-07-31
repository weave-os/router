/**
 * Wooly's animation-only terminal component, extracted from Loom's default
 * text renderer. The coaching reader, narration state, high-resolution image
 * transport, and video-game dialogue box deliberately do not live in this
 * extension.
 */

import type { Component, TUI } from "@mariozechner/pi-tui";

type Rgb = readonly [red: number, green: number, blue: number];
type Pixel = Rgb | null;
type Canvas = Pixel[][];
type WoolyAnimation = "wave" | "jump" | "spin" | "retract";

type SpriteSpec = {
	width: number;
	pixelHeight: number;
	bodyX: number;
	bodyY: number;
	bodyRadius: number;
	legBottom: number;
	compact: boolean;
};

const RESET = "\x1b[0m";
const WOOLY_ORANGE: Rgb = [235, 73, 28];
const WOOLY_BLACK: Rgb = [34, 29, 27];
const HORIZONTAL_SUBPIXELS = 2;
const QUADRANT_GLYPHS = [" ", "▘", "▝", "▀", "▖", "▌", "▞", "▛", "▗", "▚", "▐", "▜", "▄", "▙", "▟", "█"] as const;
const FULL_SPEC: SpriteSpec = {
	width: 21,
	pixelHeight: 14,
	bodyX: 10,
	bodyY: 5,
	bodyRadius: 4.5,
	legBottom: 13,
	compact: false,
};
const COMPACT_SPEC: SpriteSpec = {
	width: 15,
	pixelHeight: 10,
	bodyX: 7,
	bodyY: 3.5,
	bodyRadius: 3,
	legBottom: 9,
	compact: true,
};
const FRAME_COUNT = 8;
const ANIMATION_SEQUENCE: readonly WoolyAnimation[] = ["wave", "jump", "spin", "retract"];
const WAVE_LIFT = [0, 0.45, 1, 1, 1, 1, 0.55, 0.2] as const;
const WAVE_SWAY = [0, 0, -1, 1, -1, 1, 0, 0] as const;
const JUMP_OFFSETS = [0, 1, 1, 1, 1, 1, 1, 0] as const;
const JUMP_TUCK = [0, 0, 1, 1, 1, 1, 0, 0] as const;
const RETRACT_AMOUNT = [0, 0.35, 0.7, 1, 1, 0.7, 0.35, 0] as const;
const MIN_VISIBLE_WIDTH = 20;
const MIN_VISIBLE_ROWS = 18;
const MIN_FULL_WIDTH = 44;
const MIN_FULL_ROWS = 30;
const LEFT_PADDING = 2;

export const WOOLY_FRAME_INTERVAL_MS = 200;
export const WOOLY_ANIMATION_DELAY_MS = 5_000;

function foreground([red, green, blue]: Rgb): string {
	return `\x1b[38;2;${red};${green};${blue}m`;
}

function background([red, green, blue]: Rgb): string {
	return `\x1b[48;2;${red};${green};${blue}m`;
}

function colorsMatch(first: Rgb, second: Rgb): boolean {
	return first[0] === second[0] && first[1] === second[1] && first[2] === second[2];
}

function averageColors(colors: Rgb[]): Rgb {
	const totals = colors.reduce(
		(sum, color) => [sum[0] + color[0], sum[1] + color[1], sum[2] + color[2]],
		[0, 0, 0],
	);
	return [
		Math.round(totals[0] / colors.length),
		Math.round(totals[1] / colors.length),
		Math.round(totals[2] / colors.length),
	];
}

function colorDistance(first: Rgb, second: Rgb): number {
	return (first[0] - second[0]) ** 2 + (first[1] - second[1]) ** 2 + (first[2] - second[2]) ** 2;
}

function renderQuadrantBlock(topLeft: Pixel, topRight: Pixel, bottomLeft: Pixel, bottomRight: Pixel): string {
	const pixels = [topLeft, topRight, bottomLeft, bottomRight] as const;
	let occupiedMask = 0;
	let featureMask = 0;
	const yarnColors: Rgb[] = [];
	for (let index = 0; index < pixels.length; index++) {
		const pixel = pixels[index];
		if (pixel === null) continue;
		occupiedMask |= 1 << index;
		if (colorsMatch(pixel, WOOLY_BLACK)) featureMask |= 1 << index;
		else yarnColors.push(pixel);
	}

	if (occupiedMask === 0) return " ";
	if (occupiedMask !== 0b1111) {
		if (featureMask !== 0) return `${foreground(WOOLY_BLACK)}${QUADRANT_GLYPHS[featureMask]}${RESET}`;
		return `${foreground(averageColors(yarnColors))}${QUADRANT_GLYPHS[occupiedMask]}${RESET}`;
	}
	if (featureMask !== 0) {
		if (featureMask === 0b1111) return `${foreground(WOOLY_BLACK)}█${RESET}`;
		return `${foreground(WOOLY_BLACK)}${background(averageColors(yarnColors))}${QUADRANT_GLYPHS[featureMask]}${RESET}`;
	}

	let darkest = yarnColors[0]!;
	let lightest = yarnColors[0]!;
	for (const color of yarnColors.slice(1)) {
		const luminance = color[0] * 0.2126 + color[1] * 0.7152 + color[2] * 0.0722;
		const darkestLuminance = darkest[0] * 0.2126 + darkest[1] * 0.7152 + darkest[2] * 0.0722;
		const lightestLuminance = lightest[0] * 0.2126 + lightest[1] * 0.7152 + lightest[2] * 0.0722;
		if (luminance < darkestLuminance) darkest = color;
		if (luminance > lightestLuminance) lightest = color;
	}
	if (colorsMatch(darkest, lightest)) return `${foreground(darkest)}█${RESET}`;

	let darkMask = 0;
	for (let index = 0; index < pixels.length; index++) {
		const pixel = pixels[index]!;
		if (colorDistance(pixel, darkest) <= colorDistance(pixel, lightest)) darkMask |= 1 << index;
	}
	if (darkMask === 0 || darkMask === 0b1111) return `${foreground(averageColors(yarnColors))}█${RESET}`;
	return `${foreground(darkest)}${background(lightest)}${QUADRANT_GLYPHS[darkMask]}${RESET}`;
}

function createCanvas(spec: SpriteSpec): Canvas {
	return Array.from({ length: spec.pixelHeight }, () => Array<Pixel>(spec.width * HORIZONTAL_SUBPIXELS).fill(null));
}

function scaleX(value: number): number {
	return value * HORIZONTAL_SUBPIXELS;
}

function interpolate(from: number, to: number, amount: number): number {
	return from + (to - from) * amount;
}

function paint(canvas: Canvas, x: number, y: number, color: Rgb): void {
	if (y < 0 || y >= canvas.length || x < 0 || x >= canvas[0]!.length) return;
	canvas[y]![x] = color;
}

function drawLine(canvas: Canvas, fromX: number, fromY: number, toX: number, toY: number, color: Rgb): void {
	let x = Math.round(fromX);
	let y = Math.round(fromY);
	const endX = Math.round(toX);
	const endY = Math.round(toY);
	const deltaX = Math.abs(endX - x);
	const stepX = x < endX ? 1 : -1;
	const deltaY = -Math.abs(endY - y);
	const stepY = y < endY ? 1 : -1;
	let error = deltaX + deltaY;

	while (true) {
		paint(canvas, x, y, color);
		if (x === endX && y === endY) return;
		const doubledError = error * 2;
		if (doubledError >= deltaY) {
			error += deltaY;
			x += stepX;
		}
		if (doubledError <= deltaX) {
			error += deltaX;
			y += stepY;
		}
	}
}

function clampChannel(value: number): number {
	return Math.max(0, Math.min(255, Math.round(value)));
}

function shadeOrange(amount: number): Rgb {
	return [
		clampChannel(WOOLY_ORANGE[0] + amount),
		clampChannel(WOOLY_ORANGE[1] + amount * 0.62),
		clampChannel(WOOLY_ORANGE[2] + amount * 0.28),
	];
}

function drawLimbs(
	canvas: Canvas,
	spec: SpriteSpec,
	spinFrame: number,
	waveFrame: number,
	jumpFrame: number,
	retractFrame: number,
): void {
	const angle = (spinFrame * Math.PI) / 4;
	const turn = Math.sin(angle);
	const bob = spinFrame % 2;
	const radius = spec.bodyRadius;
	const shoulderY = spec.bodyY;
	const elbowY = spec.bodyY + radius * 0.35;
	const handY = Math.min(spec.legBottom - 1, spec.bodyY + radius * 0.92 + bob);
	const armReach = (spec.compact ? 2.1 : 3.4) - 1;
	const sway = turn * (spec.compact ? 0.7 : 1.2);
	const waveLift = WAVE_LIFT[waveFrame] ?? 0;
	const waveSway = (WAVE_SWAY[waveFrame] ?? 0) * (spec.compact ? 0.45 : 0.75);
	const retractAmount = RETRACT_AMOUNT[retractFrame] ?? 0;
	if (retractAmount === 1) return;

	const leftShoulderX = spec.bodyX - radius + 1;
	const leftElbowX = interpolate(spec.bodyX - radius - 1, leftShoulderX, retractAmount);
	const leftElbowY = interpolate(elbowY, shoulderY, retractAmount);
	const leftHandX = interpolate(spec.bodyX - radius - armReach + sway, leftShoulderX, retractAmount);
	const leftHandY = interpolate(handY, shoulderY, retractAmount);
	drawLine(canvas, scaleX(leftShoulderX), shoulderY, scaleX(leftElbowX), leftElbowY, WOOLY_BLACK);
	drawLine(canvas, scaleX(leftElbowX), leftElbowY, scaleX(leftHandX), leftHandY, WOOLY_BLACK);

	const normalRightElbowX = spec.bodyX + radius + 1;
	const normalRightHandX = spec.bodyX + radius + armReach + sway;
	const raisedRightElbowX = spec.bodyX + radius + 1;
	const raisedRightElbowY = spec.bodyY - radius * 0.05;
	const raisedRightHandX = spec.bodyX + radius + armReach * 0.65 + waveSway;
	const raisedRightHandY = spec.bodyY - radius * 0.82;
	const rightShoulderX = spec.bodyX + radius - 1;
	const animatedRightElbowX = interpolate(normalRightElbowX, raisedRightElbowX, waveLift);
	const animatedRightElbowY = interpolate(elbowY, raisedRightElbowY, waveLift);
	const animatedRightHandX = interpolate(normalRightHandX, raisedRightHandX, waveLift);
	const animatedRightHandY = interpolate(handY, raisedRightHandY, waveLift);
	const rightElbowX = interpolate(animatedRightElbowX, rightShoulderX, retractAmount);
	const rightElbowY = interpolate(animatedRightElbowY, shoulderY, retractAmount);
	const rightHandX = interpolate(animatedRightHandX, rightShoulderX, retractAmount);
	const rightHandY = interpolate(animatedRightHandY, shoulderY, retractAmount);
	drawLine(canvas, scaleX(rightShoulderX), shoulderY, scaleX(rightElbowX), rightElbowY, WOOLY_BLACK);
	drawLine(canvas, scaleX(rightElbowX), rightElbowY, scaleX(rightHandX), rightHandY, WOOLY_BLACK);

	const legTop = spec.bodyY + radius - 1;
	const legBottom = interpolate(spec.legBottom - (JUMP_TUCK[jumpFrame] ?? 0), legTop, retractAmount);
	const legSpread = spec.compact ? 1.45 : 2.35;
	const legShift = turn * (spec.compact ? 0.45 : 0.8);
	const leftFootX = scaleX(interpolate(spec.bodyX - legSpread + legShift, spec.bodyX, retractAmount));
	const rightFootX = scaleX(interpolate(spec.bodyX + legSpread + legShift, spec.bodyX, retractAmount));
	const footReach = HORIZONTAL_SUBPIXELS * (1 - retractAmount);
	drawLine(canvas, leftFootX, legTop, leftFootX, legBottom, WOOLY_BLACK);
	drawLine(canvas, rightFootX, legTop, rightFootX, legBottom, WOOLY_BLACK);
	drawLine(canvas, leftFootX, legBottom, leftFootX - footReach, legBottom, WOOLY_BLACK);
	drawLine(canvas, rightFootX, legBottom, rightFootX + footReach, legBottom, WOOLY_BLACK);
}

function drawYarnBody(canvas: Canvas, spec: SpriteSpec, frame: number): void {
	const angle = (frame * Math.PI) / 4;
	for (let y = 0; y < spec.pixelHeight; y++) {
		for (let x = 0; x < spec.width * HORIZONTAL_SUBPIXELS; x++) {
			const logicalX = x / HORIZONTAL_SUBPIXELS;
			const normalizedX = (logicalX - spec.bodyX) / spec.bodyRadius;
			const normalizedY = (y - spec.bodyY) / spec.bodyRadius;
			const distanceSquared = normalizedX ** 2 + normalizedY ** 2;
			if (distanceSquared > 1) continue;

			const edgeShade = -23 * Math.max(0, Math.sqrt(distanceSquared) - 0.58);
			const lightShade = -normalizedX * 13 - normalizedY * 10;
			const yarnWave =
				Math.sin(logicalX * 0.92 + y * 1.47 + angle * 1.8) +
				Math.sin(logicalX * 1.73 - y * 0.61 - angle * 1.15) * 0.65;
			const yarnShade = yarnWave > 0.9 ? 20 : yarnWave < -0.9 ? -18 : yarnWave * 5;
			paint(canvas, x, y, shadeOrange(edgeShade + lightShade + yarnShade));
		}
	}
}

function drawEye(canvas: Canvas, centerX: number, centerY: number): void {
	const left = Math.round(centerX - 0.5);
	paint(canvas, left, Math.round(centerY), WOOLY_BLACK);
	paint(canvas, left + 1, Math.round(centerY), WOOLY_BLACK);
}

function drawSmile(canvas: Canvas, centerX: number, centerY: number): void {
	const roundedX = Math.round(centerX);
	const roundedY = Math.round(centerY);
	paint(canvas, roundedX - 2, roundedY, WOOLY_BLACK);
	for (let offset = -1; offset <= 1; offset++) paint(canvas, roundedX + offset, roundedY + 1, WOOLY_BLACK);
	paint(canvas, roundedX + 2, roundedY, WOOLY_BLACK);
}

function drawFace(canvas: Canvas, spec: SpriteSpec, frame: number): void {
	if (frame >= 3 && frame <= 5) return;
	const angle = (frame * Math.PI) / 4;
	const faceX = scaleX(spec.bodyX - Math.sin(angle) * spec.bodyRadius * 0.58);
	const eyeY = spec.bodyY - spec.bodyRadius * 0.4;
	const mouthY = spec.bodyY + spec.bodyRadius * 0.04 - 1;
	if (frame === 2 || frame === 6) {
		drawEye(canvas, faceX, eyeY);
		drawSmile(canvas, faceX, mouthY);
		return;
	}

	const eyeSpacing = scaleX(frame === 0 ? 2 : 1);
	drawEye(canvas, faceX - eyeSpacing, eyeY);
	drawEye(canvas, faceX + eyeSpacing, eyeY);
	drawSmile(canvas, faceX, mouthY);
}

function liftCanvas(canvas: Canvas, offset: number): Canvas {
	if (offset === 0) return canvas;
	return canvas.map((row, y) => canvas[y + offset]?.slice() ?? Array<Pixel>(row.length).fill(null));
}

function buildSprite(spec: SpriteSpec, animation: WoolyAnimation | null, frame: number): string[] {
	const spinFrame = animation === "spin" ? frame : 0;
	const waveFrame = animation === "wave" ? frame : 0;
	const jumpFrame = animation === "jump" ? frame : 0;
	const retractFrame = animation === "retract" ? frame : 0;
	const canvas = createCanvas(spec);
	drawLimbs(canvas, spec, spinFrame, waveFrame, jumpFrame, retractFrame);
	drawYarnBody(canvas, spec, spinFrame);
	drawFace(canvas, spec, spinFrame);
	const renderedCanvas = liftCanvas(canvas, JUMP_OFFSETS[jumpFrame] ?? 0);

	const lines: string[] = [];
	for (let y = 0; y < spec.pixelHeight; y += 2) {
		let line = "";
		for (let x = 0; x < spec.width; x++) {
			const pixelX = x * HORIZONTAL_SUBPIXELS;
			line += renderQuadrantBlock(
				renderedCanvas[y]![pixelX]!,
				renderedCanvas[y]![pixelX + 1]!,
				renderedCanvas[y + 1]?.[pixelX] ?? null,
				renderedCanvas[y + 1]?.[pixelX + 1] ?? null,
			);
		}
		lines.push(line);
	}
	return lines;
}

export class WoolyComponent implements Component {
	private interval: ReturnType<typeof setInterval> | undefined;
	private frame = 0;
	private idleElapsedMs = 0;
	private animation: WoolyAnimation | null = null;
	private nextAnimationIndex = 0;
	private cachedKey = "";
	private cachedLines: string[] = [];

	constructor(private readonly ui: TUI) {
		this.interval = setInterval(() => {
			if (!this.advanceAnimation()) return;
			this.cachedKey = "";
			this.ui.requestRender();
		}, WOOLY_FRAME_INTERVAL_MS);
		this.interval.unref();
	}

	private advanceAnimation(): boolean {
		if (this.animation === null) {
			this.idleElapsedMs += WOOLY_FRAME_INTERVAL_MS;
			if (this.idleElapsedMs < WOOLY_ANIMATION_DELAY_MS) return false;
			this.animation = ANIMATION_SEQUENCE[this.nextAnimationIndex]!;
			this.nextAnimationIndex = (this.nextAnimationIndex + 1) % ANIMATION_SEQUENCE.length;
			this.frame = 1;
			return true;
		}
		if (this.frame < FRAME_COUNT - 1) {
			this.frame++;
		} else {
			this.frame = 0;
			this.animation = null;
			this.idleElapsedMs = 0;
		}
		return true;
	}

	invalidate(): void {
		this.cachedKey = "";
	}

	render(width: number): string[] {
		const rows = this.ui.terminal.rows;
		const mode =
			width < MIN_VISIBLE_WIDTH || rows < MIN_VISIBLE_ROWS
				? "hidden"
				: width < MIN_FULL_WIDTH || rows < MIN_FULL_ROWS
					? "compact"
					: "full";
		const cacheKey = `${mode}:${width}:${rows}:${this.animation ?? "idle"}:${this.frame}`;
		if (cacheKey === this.cachedKey) return this.cachedLines;
		if (mode === "hidden") {
			this.cachedLines = [];
			this.cachedKey = cacheKey;
			return this.cachedLines;
		}

		const spec = mode === "full" ? FULL_SPEC : COMPACT_SPEC;
		const sprite = buildSprite(spec, this.animation, this.frame);
		const leftPadding = Math.max(0, Math.min(LEFT_PADDING, width - spec.width));
		this.cachedLines = sprite.map((line) => `${" ".repeat(leftPadding)}${line}`);
		this.cachedKey = cacheKey;
		return this.cachedLines;
	}

	dispose(): void {
		if (this.interval !== undefined) {
			clearInterval(this.interval);
			this.interval = undefined;
		}
	}
}
