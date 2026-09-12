import { Injectable, computed, inject, signal } from '@angular/core';
import { firstValueFrom } from 'rxjs';
import { ApiService } from './api.service';
import { AudioExtractorService } from './audio-extractor.service';
import { FrameExtractorService } from './frame-extractor.service';
import { parseImmichInput } from './immich-input';
import { SegmentUploadEvent, TusUploadService, UploadController, priorityOf } from './tus-upload.service';
import { Job, LanguageOptions } from '../models/job.model';
import { TimelineSegment } from '../models/timeline-segment.model';

export type Phase =
  | 'idle'
  | 'reading'
  | 'overview'
  | 'uploading'
  | 'importing' // origin=immich: server is downloading + building the collage
  | 'complete'
  | 'error';

const POLL_MS = 2000;

/**
 * Owns the whole client-side pipeline for one job and exposes it as signals the
 * components render from: macro collage -> create job -> overview -> (audio ||
 * prioritised tus upload) -> poll until the backend marks the job complete.
 */
@Injectable({ providedIn: 'root' })
export class JobStore {
  private api = inject(ApiService);
  private frames = inject(FrameExtractorService);
  private audio = inject(AudioExtractorService);
  private tus = inject(TusUploadService);

  readonly job = signal<Job | null>(null);
  readonly phase = signal<Phase>('idle');
  readonly error = signal<string | null>(null);
  readonly audioPhase = signal<'idle' | 'extracting' | 'sent' | 'error'>('idle');
  readonly uploads = signal<Record<string, SegmentUploadEvent>>({});

  readonly segments = computed<TimelineSegment[]>(() => this.job()?.segments ?? []);
  readonly overviewStory = computed(() => this.job()?.overviewStory ?? '');
  readonly transcript = computed(() => this.job()?.transcript ?? '');
  readonly chatMessages = computed(() => this.job()?.chatMessages ?? []);
  readonly events = computed(() => this.job()?.events ?? []);

  private controller: UploadController | null = null;
  private pollTimer: ReturnType<typeof setInterval> | null = null;

  async run(file: File, lang: LanguageOptions = {}): Promise<void> {
    this.reset();
    try {
      this.phase.set('reading');
      const macro = await this.frames.extract(file);

      const created = await firstValueFrom(
        this.api.createJob({
          filename: file.name,
          sizeBytes: file.size,
          durationSec: macro.durationSec,
          mimeType: file.type || undefined,
          ...lang,
        }),
      );
      this.job.set(created.job);

      this.phase.set('overview');
      const withOverview = await firstValueFrom(
        this.api.postOverview(created.job.id, macro.blob, macro.meta),
      );
      this.job.set(withOverview);

      // Stage 2 runs in the background; failure is non-fatal.
      void this.runAudio(file, created.job.id);

      // Stage 3: prioritised upload.
      this.phase.set('uploading');
      this.controller = this.tus.start(file, this.job()!, {
        // always set for the local-upload path this method is exclusive to
        endpoint: created.tusEndpoint ?? '/files/',
        onEvent: (e) => this.onUploadEvent(e),
        onAllDone: () => void firstValueFrom(this.api.finalize(created.job.id)).catch(() => {}),
      });

      this.startPolling(created.job.id);
    } catch (err) {
      this.error.set(err instanceof Error ? err.message : String(err));
      this.phase.set('error');
    }
  }

  /** Import a video directly from the user's own Immich instance by asset id or
   *  share link — no local file, no browser upload. The backend resolves the
   *  asset, downloads it, and builds the macro collage + audio itself. */
  async runFromImmich(input: string, lang: LanguageOptions = {}, autoWriteback = false): Promise<void> {
    this.reset();
    try {
      this.phase.set('importing');
      const created = await firstValueFrom(
        this.api.createJobFromImmich({ ...parseImmichInput(input), ...lang, autoWriteback }),
      );
      this.job.set(created.job);
      this.startPolling(created.job.id);
    } catch (err) {
      this.error.set(err instanceof Error ? err.message : String(err));
      this.phase.set('error');
    }
  }

  /** Load a previously created job (from the "previous videos" timeline) by
   *  id — no upload/extraction, just fetch and display. Resumes polling if
   *  the job is somehow still mid-pipeline (e.g. reloading the page while an
   *  Immich import is running), otherwise just shows the finished state. */
  async loadJob(id: string): Promise<void> {
    this.reset();
    try {
      const fresh = await firstValueFrom(this.api.getJob(id));
      this.job.set(fresh);
      if (fresh.status === 'complete' || fresh.status === 'error') {
        this.phase.set(fresh.status === 'complete' ? 'complete' : 'error');
      } else {
        this.phase.set('uploading');
        this.startPolling(id);
      }
    } catch (err) {
      this.error.set(err instanceof Error ? err.message : String(err));
      this.phase.set('error');
    }
  }

  /** Manually (re-)trigger writing the finished analysis back onto the
   *  source Immich asset — the button used when the job wasn't imported with
   *  autoWriteback on. The backend runs it best-effort in the background, and
   *  by the time this is used the main poll loop has normally already
   *  stopped (job is complete), so this runs its own short poll to pick up
   *  immichWrittenBack flipping true — same pattern as waitForChatReply. */
  async triggerImmichWriteback(): Promise<void> {
    const j = this.job();
    if (!j) return;
    try {
      const updated = await firstValueFrom(this.api.postImmichWriteback(j.id));
      this.mergeServerJob(updated);
      await this.waitForImmichWriteback(j.id);
    } catch (err) {
      this.error.set(`immich write-back failed: ${err instanceof Error ? err.message : err}`);
    }
  }

  private async waitForImmichWriteback(jobId: string): Promise<void> {
    const deadline = Date.now() + 30000;
    while (Date.now() < deadline) {
      const fresh = await firstValueFrom(this.api.getJob(jobId)).catch(() => null);
      if (fresh) {
        this.mergeServerJob(fresh);
        if (fresh.immichWrittenBack) return;
      }
      await new Promise((r) => setTimeout(r, 1500));
    }
  }

  /** Ask a follow-up question about the current job. The backend persists the
   *  question synchronously (reflected in the response immediately) and
   *  appends the reply asynchronously. Chat is normally used *after* the job
   *  is already "complete", at which point the main poll loop has already
   *  stopped (see startPolling) — so this runs its own short-lived poll to
   *  pick up the reply instead of assuming the main one is still running. */
  async sendChatMessage(text: string): Promise<void> {
    const j = this.job();
    const message = text.trim();
    if (!j || !message) return;
    try {
      const updated = await firstValueFrom(this.api.postChat(j.id, message));
      this.mergeServerJob(updated);
      await this.waitForChatReply(j.id, updated.chatMessages?.length ?? 0);
    } catch (err) {
      this.error.set(`chat failed: ${err instanceof Error ? err.message : err}`);
    }
  }

  private async waitForChatReply(jobId: string, countAfterQuestion: number): Promise<void> {
    const deadline = Date.now() + 60000;
    while (Date.now() < deadline) {
      const fresh = await firstValueFrom(this.api.getJob(jobId)).catch(() => null);
      if (fresh) {
        this.mergeServerJob(fresh);
        if ((fresh.chatMessages?.length ?? 0) > countAfterQuestion) return;
      }
      await new Promise((r) => setTimeout(r, 1500));
    }
  }

  /** User flags a segment as interesting: raise its upload priority + ask the
   *  backend to run granular analysis as soon as its bytes are in. */
  async markInteresting(seg: TimelineSegment): Promise<void> {
    const j = this.job();
    if (!j) return;
    const optimistic: TimelineSegment = { ...seg, source: 'user', interestScore: Math.max(seg.interestScore, 0.9) };
    this.patchSegment(optimistic);
    this.controller?.bump(seg.id, priorityOf(optimistic));
    try {
      await firstValueFrom(this.api.processSegment(j.id, seg.id, { source: 'user' }));
    } catch (err) {
      this.error.set(`process segment failed: ${err instanceof Error ? err.message : err}`);
    }
  }

  stop(): void {
    this.controller?.abort();
    if (this.pollTimer) clearInterval(this.pollTimer);
    this.pollTimer = null;
  }

  // --- internals ------------------------------------------------------------

  private async runAudio(file: File, jobId: string): Promise<void> {
    try {
      this.audioPhase.set('extracting');
      const track = await this.audio.extract(file);
      await firstValueFrom(this.api.postAudio(jobId, track.blob));
      this.audioPhase.set('sent');
    } catch {
      this.audioPhase.set('error');
    }
  }

  private onUploadEvent(e: SegmentUploadEvent): void {
    this.uploads.update((m) => ({ ...m, [e.segmentId]: e }));
    const j = this.job();
    if (!j) return;
    const seg = j.segments.find((s) => s.id === e.segmentId);
    if (seg) {
      this.patchSegment({
        ...seg,
        uploadedBytes: e.uploadedBytes,
        status:
          e.status === 'uploaded'
            ? 'uploaded'
            : e.status === 'error'
              ? 'error'
              : 'uploading',
      });
    }
  }

  private startPolling(jobId: string): void {
    this.pollTimer = setInterval(async () => {
      try {
        const fresh = await firstValueFrom(this.api.getJob(jobId));
        this.mergeServerJob(fresh);
        if (fresh.status === 'complete' || fresh.status === 'error') {
          this.phase.set(fresh.status === 'complete' ? 'complete' : 'error');
          this.stop();
        }
      } catch {
        /* transient; keep polling */
      }
    }, POLL_MS);
  }

  /** Server is authoritative for analysis fields; local upload progress wins for
   *  uploadedBytes so the bar doesn't jump backwards between polls. */
  private mergeServerJob(fresh: Job): void {
    const local = this.job();
    const localUploads = this.uploads();
    fresh.segments = fresh.segments.map((s) => {
      const l = local?.segments.find((x) => x.id === s.id);
      const up = localUploads[s.id];
      const uploadedBytes = Math.max(s.uploadedBytes, up?.uploadedBytes ?? 0, l?.uploadedBytes ?? 0);
      return { ...s, uploadedBytes };
    });
    this.job.set(fresh);
  }

  private patchSegment(seg: TimelineSegment): void {
    const j = this.job();
    if (!j) return;
    this.job.set({
      ...j,
      segments: j.segments.map((s) => (s.id === seg.id ? { ...s, ...seg } : s)),
    });
  }

  private reset(): void {
    this.stop();
    this.controller = null;
    this.job.set(null);
    this.error.set(null);
    this.uploads.set({});
    this.audioPhase.set('idle');
    this.phase.set('idle');
  }
}
