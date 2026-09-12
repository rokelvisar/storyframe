import { Injectable } from '@angular/core';
import * as tus from 'tus-js-client';
import { Job } from '../models/job.model';
import { TimelineSegment } from '../models/timeline-segment.model';
import { PriorityQueue } from './priority-queue';
import { priorityOf } from './upload-priority';

export { priorityOf } from './upload-priority';

export interface SegmentUploadEvent {
  segmentId: string;
  index: number;
  uploadedBytes: number;
  totalBytes: number;
  status: 'uploading' | 'uploaded' | 'error';
  error?: string;
}

export interface UploadControllerOptions {
  endpoint: string;
  maxConcurrent?: number;
  chunkSize?: number;
  onEvent: (e: SegmentUploadEvent) => void;
  onAllDone?: () => void;
}

interface ChunkTask {
  segment: TimelineSegment;
  priority: number;
}

/**
 * Uploads a large local file as one independent (resumable) tus upload per
 * timeline segment. The order segments are started in is driven by a max-priority
 * queue, so segments the user or the VLM flag as interesting reach the server
 * first; the Go backend reassembles the parts in index order for the full archive.
 */
@Injectable({ providedIn: 'root' })
export class TusUploadService {
  start(file: File, job: Job, opts: UploadControllerOptions): UploadController {
    return new UploadController(file, job, opts);
  }
}

export class UploadController {
  private queue = new PriorityQueue<ChunkTask>();
  private active = new Map<string, tus.Upload>();
  private started = new Set<string>();
  private finished = new Set<string>();
  private readonly maxConcurrent: number;
  private readonly chunkSize: number;
  private stopped = false;

  constructor(
    private file: File,
    private job: Job,
    private opts: UploadControllerOptions,
  ) {
    this.maxConcurrent = opts.maxConcurrent ?? 3;
    this.chunkSize = opts.chunkSize ?? 8 * 1024 * 1024;
    for (const seg of job.segments) {
      this.queue.push({ segment: seg, priority: priorityOf(seg) }, priorityOf(seg));
    }
    this.pump();
  }

  /** Raise a segment's priority (user clicked it / VLM scored it). */
  bump(segmentId: string, priority: number): void {
    this.queue.reprioritize((t) => t.segment.id === segmentId, priority);
    this.pump();
  }

  /** Current pending order, highest priority first (for UI / tests). */
  pendingOrder(): string[] {
    return this.queue.toArray().map((t) => t.segment.id);
  }

  abort(): void {
    this.stopped = true;
    for (const up of this.active.values()) void up.abort();
    this.active.clear();
  }

  private pump(): void {
    if (this.stopped) return;
    while (this.active.size < this.maxConcurrent) {
      const task = this.queue.pop();
      if (!task) break;
      if (this.started.has(task.segment.id)) continue;
      this.startTask(task);
    }
  }

  private startTask(task: ChunkTask): void {
    const seg = task.segment;
    this.started.add(seg.id);
    const slice = this.file.slice(seg.byteStart, seg.byteEnd);
    const total = seg.byteEnd - seg.byteStart;

    const upload = new tus.Upload(slice, {
      endpoint: this.opts.endpoint,
      chunkSize: this.chunkSize,
      retryDelays: [0, 1000, 3000, 5000, 10000],
      removeFingerprintOnSuccess: true,
      metadata: {
        jobId: this.job.id,
        segmentId: seg.id,
        partIndex: String(seg.index),
        filename: this.job.filename,
        segmentStartSec: String(seg.startSec),
        segmentEndSec: String(seg.endSec),
      },
      onProgress: (sent) => {
        this.opts.onEvent({
          segmentId: seg.id,
          index: seg.index,
          uploadedBytes: sent,
          totalBytes: total,
          status: 'uploading',
        });
      },
      onSuccess: () => {
        this.active.delete(seg.id);
        this.finished.add(seg.id);
        this.opts.onEvent({
          segmentId: seg.id,
          index: seg.index,
          uploadedBytes: total,
          totalBytes: total,
          status: 'uploaded',
        });
        if (this.finished.size === this.job.segments.length) this.opts.onAllDone?.();
        this.pump();
      },
      onError: (err) => {
        this.active.delete(seg.id);
        this.started.delete(seg.id);
        this.opts.onEvent({
          segmentId: seg.id,
          index: seg.index,
          uploadedBytes: 0,
          totalBytes: total,
          status: 'error',
          error: String(err),
        });
        // leave it out of the queue; caller can bump() to retry
        this.pump();
      },
    });

    this.active.set(seg.id, upload);
    upload.start();
  }
}
