import { describe, expect, it } from 'vitest';
import { priorityOf } from './upload-priority';
import { PriorityQueue } from './priority-queue';

const seg = (over: Partial<Parameters<typeof priorityOf>[0]> = {}) => ({
  source: 'plan' as const,
  interestScore: 0,
  index: 0,
  ...over,
});

describe('priorityOf', () => {
  it('ranks user picks above AI interest above plan order', () => {
    const user = priorityOf(seg({ source: 'user', index: 40 }));
    const ai = priorityOf(seg({ source: 'ai', interestScore: 1, index: 1 }));
    const plan = priorityOf(seg({ source: 'plan', interestScore: 1, index: 1 }));
    expect(user).toBeGreaterThan(ai);
    expect(ai).toBeGreaterThan(plan);
  });

  it('within a tier, higher interest wins and earlier index breaks ties', () => {
    expect(priorityOf(seg({ source: 'ai', interestScore: 0.9 }))).toBeGreaterThan(
      priorityOf(seg({ source: 'ai', interestScore: 0.3 })),
    );
    expect(priorityOf(seg({ source: 'ai', interestScore: 0.5, index: 2 }))).toBeGreaterThan(
      priorityOf(seg({ source: 'ai', interestScore: 0.5, index: 9 })),
    );
  });

  it('drives the upload queue order end to end', () => {
    const segs = [
      seg({ index: 0, source: 'plan', interestScore: 0.1 }),
      seg({ index: 1, source: 'ai', interestScore: 0.8 }),
      seg({ index: 2, source: 'plan', interestScore: 0.2 }),
      seg({ index: 3, source: 'user', interestScore: 0.9 }),
    ];
    const q = new PriorityQueue<number>();
    segs.forEach((s) => q.push(s.index, priorityOf(s)));
    expect(q.toArray()).toEqual([3, 1, 2, 0]);
  });
});
