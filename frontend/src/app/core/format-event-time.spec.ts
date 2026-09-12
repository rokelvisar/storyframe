import { describe, expect, it } from 'vitest';
import { formatEventTime } from './format-event-time';

describe('formatEventTime', () => {
  it('formats a valid ISO timestamp as a local HH:MM:SS time', () => {
    const got = formatEventTime('2026-01-01T12:34:56Z');
    expect(got).toMatch(/^\d{2}:\d{2}:\d{2}$/);
  });

  it('returns an empty string for invalid input', () => {
    expect(formatEventTime('not-a-date')).toBe('');
    expect(formatEventTime('')).toBe('');
  });
});
