import { TimelineSegment } from '../models/timeline-segment.model';

/**
 * Priority score for uploading a segment's bytes. Explicit user picks outrank
 * VLM-flagged interest, which outranks the default plan order; within a tier the
 * higher interest score wins and earlier segments break ties.
 */
export function priorityOf(seg: Pick<TimelineSegment, 'source' | 'interestScore' | 'index'>): number {
  const base = seg.source === 'user' ? 1000 : seg.source === 'ai' ? 100 : 0;
  return base + Math.round(seg.interestScore * 100) - seg.index * 0.001;
}
