import { describe, expect, it } from 'vitest';
import { formatTimecode } from './time-util';

describe('formatTimecode', () => {
  it('formats as zero-padded HH:MM:SS', () => {
    expect(formatTimecode(0)).toBe('00:00:00');
    expect(formatTimecode(9)).toBe('00:00:09');
    expect(formatTimecode(75)).toBe('00:01:15');
    expect(formatTimecode(3661)).toBe('01:01:01');
    expect(formatTimecode(7325.9)).toBe('02:02:05');
  });

  it('clamps negatives to zero', () => {
    expect(formatTimecode(-5)).toBe('00:00:00');
  });
});
