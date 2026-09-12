import { describe, expect, it } from 'vitest';
import { gridFor, planCollage } from './collage-plan';

describe('gridFor', () => {
  it('returns a near-square grid that fits all cells', () => {
    expect(gridFor(20)).toEqual({ cols: 5, rows: 4 });
    expect(gridFor(16)).toEqual({ cols: 4, rows: 4 });
    expect(gridFor(24)).toEqual({ cols: 5, rows: 5 });
    const g = gridFor(17);
    expect(g.cols * g.rows).toBeGreaterThanOrEqual(17);
  });
});

describe('planCollage', () => {
  it('clamps frame count into [minFrames, maxFrames]', () => {
    expect(planCollage(600, { targetFrames: 4 }).frames.length).toBe(16);
    expect(planCollage(600, { targetFrames: 999 }).frames.length).toBe(24);
    expect(planCollage(600, { targetFrames: 20 }).frames.length).toBe(20);
  });

  it('lays cells out left-to-right, top-to-bottom with exact pixel rects', () => {
    const p = planCollage(100, { targetFrames: 20, cellWidth: 320, aspect: 16 / 9 });
    expect(p.cols).toBe(5);
    expect(p.cellHeight).toBe(Math.round(320 / (16 / 9)));
    expect(p.canvasWidth).toBe(5 * 320);
    expect(p.canvasHeight).toBe(p.rows * p.cellHeight);

    const sixth = p.frames[5]; // index 5 -> row 1, col 0
    expect(sixth.row).toBe(1);
    expect(sixth.col).toBe(0);
    expect(sixth.x).toBe(0);
    expect(sixth.y).toBe(p.cellHeight);
    expect(sixth.w).toBe(320);

    const last = p.frames[19]; // row 3, col 4
    expect(last).toMatchObject({ row: 3, col: 4, x: 4 * 320, y: 3 * p.cellHeight });
  });

  it('samples evenly and monotonically within the edge-trimmed span', () => {
    const p = planCollage(1000, { targetFrames: 20, edgeSkipFraction: 0.01 });
    expect(p.timestamps).toHaveLength(20);
    expect(p.timestamps[0]).toBeGreaterThan(10); // past the 1% edge
    expect(p.timestamps[19]).toBeLessThan(990);
    for (let i = 1; i < p.timestamps.length; i++) {
      expect(p.timestamps[i]).toBeGreaterThan(p.timestamps[i - 1]);
    }
    const gaps = p.timestamps.slice(1).map((t, i) => t - p.timestamps[i]);
    const spread = Math.max(...gaps) - Math.min(...gaps);
    expect(spread).toBeLessThan(1e-6); // uniform spacing
  });

  it('stamps an HH:MM:SS label matching the timestamp', () => {
    const p = planCollage(7200, { targetFrames: 16 });
    expect(p.frames[0].label).toMatch(/^\d{2}:\d{2}:\d{2}$/);
  });

  it('does not divide by zero on a zero-length video', () => {
    expect(() => planCollage(0)).not.toThrow();
    expect(planCollage(0).frames.length).toBe(20);
    expect(planCollage(0).timestamps.every((t) => Number.isFinite(t))).toBe(true);
  });
});
