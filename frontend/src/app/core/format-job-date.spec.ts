import { describe, expect, it } from 'vitest';
import { formatJobDate } from './format-job-date';

describe('formatJobDate', () => {
  it('formats a valid ISO timestamp as a local date + time', () => {
    const got = formatJobDate('2026-01-01T12:34:56Z');
    expect(got.length).toBeGreaterThan(0);
    expect(got).not.toBe('Invalid Date');
  });

  it('returns an empty string for invalid input', () => {
    expect(formatJobDate('not-a-date')).toBe('');
    expect(formatJobDate('')).toBe('');
  });
});
