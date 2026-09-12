import { describe, expect, it } from 'vitest';
import { PriorityQueue } from './priority-queue';

describe('PriorityQueue', () => {
  it('pops highest priority first', () => {
    const q = new PriorityQueue<string>();
    q.push('low', 1);
    q.push('high', 10);
    q.push('mid', 5);
    expect([q.pop(), q.pop(), q.pop()]).toEqual(['high', 'mid', 'low']);
    expect(q.pop()).toBeUndefined();
  });

  it('keeps FIFO order for equal priorities', () => {
    const q = new PriorityQueue<string>();
    q.push('a', 5);
    q.push('b', 5);
    q.push('c', 5);
    expect([q.pop(), q.pop(), q.pop()]).toEqual(['a', 'b', 'c']);
  });

  it('reprioritize moves an item to the front', () => {
    const q = new PriorityQueue<string>();
    q.push('a', 1);
    q.push('b', 2);
    q.push('c', 3);
    expect(q.reprioritize((v) => v === 'a', 99)).toBe(true);
    expect(q.reprioritize((v) => v === 'zzz', 99)).toBe(false);
    expect(q.pop()).toBe('a');
  });

  it('toArray reflects priority + insertion order without mutating', () => {
    const q = new PriorityQueue<number>();
    [3, 1, 2, 1].forEach((n, i) => q.push(n * 10 + i, n));
    const order = q.toArray();
    expect(order).toHaveLength(4);
    expect(q.size).toBe(4);
    // still pops in the same order toArray reported
    expect(q.toArray()).toEqual([q.pop(), q.pop(), q.pop(), q.pop()]);
  });

  it('handles interleaved push/pop (heap integrity)', () => {
    const q = new PriorityQueue<number>();
    const ref: number[] = [];
    for (let i = 0; i < 200; i++) {
      const p = Math.floor(Math.random() * 50);
      q.push(p, p);
      ref.push(p);
      if (i % 3 === 0) {
        ref.sort((a, b) => b - a);
        expect(q.pop()).toBe(ref.shift());
      }
    }
    ref.sort((a, b) => b - a);
    while (ref.length) expect(q.pop()).toBe(ref.shift());
  });
});
