import { FrameMeta, OverviewMeta } from '../models/frame-meta.model';
import { formatTimecode } from './time-util';

export interface CollagePlanOptions {
  /** Total frames to sample (clamped to [minFrames, maxFrames]). */
  targetFrames?: number;
  minFrames?: number;
  maxFrames?: number;
  /** Scaled cell width in px; height is derived from the source aspect ratio. */
  cellWidth?: number;
  /** Source frame aspect ratio (width / height). Defaults to 16:9. */
  aspect?: number;
  /** Fraction of the video skipped at each end so we don't sample black frames. */
  edgeSkipFraction?: number;
}

export interface CollagePlan extends OverviewMeta {
  /** Absolute timestamps (seconds) to seek to, in order. */
  timestamps: number[];
  canvasWidth: number;
  canvasHeight: number;
}

const DEFAULTS: Required<CollagePlanOptions> = {
  targetFrames: 20,
  minFrames: 16,
  maxFrames: 24,
  cellWidth: 320,
  aspect: 16 / 9,
  edgeSkipFraction: 0.01,
};

/** Chooses a near-square grid that fits `count` cells. */
export function gridFor(count: number): { cols: number; rows: number } {
  const cols = Math.ceil(Math.sqrt(count));
  const rows = Math.ceil(count / cols);
  return { cols, rows };
}

/**
 * Pure planner for the Stage-1 macro collage: given a duration it returns the
 * grid, the evenly spaced timestamps to capture and the pixel rectangle +
 * stamped label for every cell. No DOM access — unit-tested in isolation.
 */
export function planCollage(durationSec: number, options: CollagePlanOptions = {}): CollagePlan {
  const o = { ...DEFAULTS, ...options };
  if (!(durationSec > 0)) {
    durationSec = 1;
  }

  const count = Math.max(o.minFrames, Math.min(o.maxFrames, Math.round(o.targetFrames)));
  const { cols, rows } = gridFor(count);
  const cellWidth = Math.round(o.cellWidth);
  const cellHeight = Math.round(cellWidth / o.aspect);

  const lo = durationSec * o.edgeSkipFraction;
  const hi = durationSec * (1 - o.edgeSkipFraction);
  const span = Math.max(0, hi - lo);

  const timestamps: number[] = [];
  const frames: FrameMeta[] = [];
  for (let i = 0; i < count; i++) {
    // sample at the centre of each of `count` equal slices
    const t = count === 1 ? lo + span / 2 : lo + (span * (i + 0.5)) / count;
    const row = Math.floor(i / cols);
    const col = i % cols;
    timestamps.push(round3(t));
    frames.push({
      t: round3(t),
      row,
      col,
      x: col * cellWidth,
      y: row * cellHeight,
      w: cellWidth,
      h: cellHeight,
      label: formatTimecode(t),
    });
  }

  return {
    cols,
    rows,
    cellWidth,
    cellHeight,
    frames,
    timestamps,
    canvasWidth: cols * cellWidth,
    canvasHeight: rows * cellHeight,
  };
}

function round3(n: number): number {
  return Math.round(n * 1000) / 1000;
}
