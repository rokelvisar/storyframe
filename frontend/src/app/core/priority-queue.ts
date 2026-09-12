/**
 * Small binary max-heap keyed by a numeric priority. Ties keep FIFO order via a
 * monotonic sequence number, so equal-priority chunks upload in timeline order.
 */
export class PriorityQueue<T> {
  private heap: { priority: number; seq: number; value: T }[] = [];
  private seq = 0;

  get size(): number {
    return this.heap.length;
  }

  push(value: T, priority: number): void {
    this.heap.push({ priority, seq: this.seq++, value });
    this.bubbleUp(this.heap.length - 1);
  }

  /** Removes and returns the highest-priority item, or undefined when empty. */
  pop(): T | undefined {
    if (this.heap.length === 0) return undefined;
    const top = this.heap[0];
    const last = this.heap.pop()!;
    if (this.heap.length > 0) {
      this.heap[0] = last;
      this.bubbleDown(0);
    }
    return top.value;
  }

  peek(): T | undefined {
    return this.heap[0]?.value;
  }

  /** Re-prioritises the first item matching `match`; returns true if found. */
  reprioritize(match: (v: T) => boolean, priority: number): boolean {
    const i = this.heap.findIndex((n) => match(n.value));
    if (i < 0) return false;
    const old = this.heap[i].priority;
    this.heap[i].priority = priority;
    if (priority > old) this.bubbleUp(i);
    else this.bubbleDown(i);
    return true;
  }

  toArray(): T[] {
    return [...this.heap]
      .sort((a, b) => b.priority - a.priority || a.seq - b.seq)
      .map((n) => n.value);
  }

  private higher(a: number, b: number): boolean {
    return (
      this.heap[a].priority > this.heap[b].priority ||
      (this.heap[a].priority === this.heap[b].priority && this.heap[a].seq < this.heap[b].seq)
    );
  }

  private bubbleUp(i: number): void {
    while (i > 0) {
      const parent = (i - 1) >> 1;
      if (this.higher(i, parent)) {
        [this.heap[i], this.heap[parent]] = [this.heap[parent], this.heap[i]];
        i = parent;
      } else break;
    }
  }

  private bubbleDown(i: number): void {
    const n = this.heap.length;
    for (;;) {
      const l = 2 * i + 1;
      const r = 2 * i + 2;
      let best = i;
      if (l < n && this.higher(l, best)) best = l;
      if (r < n && this.higher(r, best)) best = r;
      if (best === i) break;
      [this.heap[i], this.heap[best]] = [this.heap[best], this.heap[i]];
      i = best;
    }
  }
}
