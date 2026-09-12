import { Injectable } from '@angular/core';
import { OverviewMeta } from '../models/frame-meta.model';
import { CollagePlanOptions, planCollage } from './collage-plan';

export interface MacroCollageResult {
  blob: Blob;
  meta: OverviewMeta;
  durationSec: number;
  width: number;
  height: number;
}

/**
 * Stage 1: reads a locally selected video with a hidden <video> element, seeks to
 * evenly spaced timestamps, paints each frame into a <canvas> grid, stamps the
 * timestamp onto every cell and returns a single JPEG macro collage (~1-2 MB)
 * plus the FrameMeta grid for VLM OCR alignment.
 */
@Injectable({ providedIn: 'root' })
export class FrameExtractorService {
  async extract(
    file: File,
    options: CollagePlanOptions & { jpegQuality?: number } = {},
  ): Promise<MacroCollageResult> {
    const url = URL.createObjectURL(file);
    const video = document.createElement('video');
    video.preload = 'metadata';
    video.muted = true;
    (video as HTMLVideoElement & { playsInline: boolean }).playsInline = true;
    video.src = url;

    try {
      await once(video, 'loadedmetadata');
      const duration = video.duration;
      const aspect =
        video.videoWidth && video.videoHeight ? video.videoWidth / video.videoHeight : 16 / 9;
      const plan = planCollage(duration, { aspect, ...options });

      const canvas = document.createElement('canvas');
      canvas.width = plan.canvasWidth;
      canvas.height = plan.canvasHeight;
      const ctx = canvas.getContext('2d');
      if (!ctx) throw new Error('2D canvas context unavailable');
      ctx.fillStyle = '#000';
      ctx.fillRect(0, 0, canvas.width, canvas.height);

      for (let i = 0; i < plan.timestamps.length; i++) {
        await seek(video, plan.timestamps[i]);
        const cell = plan.frames[i];
        ctx.drawImage(video, cell.x, cell.y, cell.w, cell.h);
        stampLabel(ctx, cell.label ?? '', cell.x, cell.y, cell.w);
      }

      const quality = options.jpegQuality ?? 0.7;
      const blob = await toBlob(canvas, 'image/jpeg', quality);
      const meta: OverviewMeta = {
        cols: plan.cols,
        rows: plan.rows,
        cellWidth: plan.cellWidth,
        cellHeight: plan.cellHeight,
        frames: plan.frames,
      };
      return { blob, meta, durationSec: duration, width: canvas.width, height: canvas.height };
    } finally {
      video.removeAttribute('src');
      video.load();
      URL.revokeObjectURL(url);
    }
  }
}

function stampLabel(ctx: CanvasRenderingContext2D, text: string, x: number, y: number, w: number): void {
  if (!text) return;
  const pad = 4;
  const fontSize = Math.max(11, Math.round(w * 0.05));
  ctx.font = `bold ${fontSize}px monospace`;
  const metrics = ctx.measureText(text);
  const boxW = metrics.width + pad * 2;
  const boxH = fontSize + pad * 2;
  ctx.fillStyle = 'rgba(0,0,0,0.55)';
  ctx.fillRect(x + 2, y + 2, boxW, boxH);
  ctx.fillStyle = '#fff';
  ctx.textBaseline = 'top';
  ctx.fillText(text, x + 2 + pad, y + 2 + pad);
}

function once(el: HTMLMediaElement, ev: string): Promise<void> {
  return new Promise((resolve, reject) => {
    const ok = () => {
      cleanup();
      resolve();
    };
    const bad = () => {
      cleanup();
      reject(new Error(`video error while waiting for ${ev}`));
    };
    const cleanup = () => {
      el.removeEventListener(ev, ok);
      el.removeEventListener('error', bad);
    };
    el.addEventListener(ev, ok, { once: true });
    el.addEventListener('error', bad, { once: true });
  });
}

function seek(video: HTMLVideoElement, t: number): Promise<void> {
  return new Promise((resolve, reject) => {
    let settled = false;
    const finish = () => {
      if (settled) return;
      settled = true;
      cleanup();
      resolve();
    };
    const onSeeked = () => {
      // The frame at the new position is decoded once 'seeked' fires. Nudge the
      // compositor with requestVideoFrameCallback when we can, but DON'T depend on
      // it: rVFC never fires for a paused <video>, which would hang extraction.
      const rvfc = (video as HTMLVideoElement & {
        requestVideoFrameCallback?: (cb: () => void) => number;
      }).requestVideoFrameCallback?.bind(video);
      if (rvfc) rvfc(() => finish());
      setTimeout(finish, 60);
    };
    const onError = () => {
      if (settled) return;
      settled = true;
      cleanup();
      reject(new Error(`seek to ${t}s failed`));
    };
    const cleanup = () => {
      video.removeEventListener('seeked', onSeeked);
      video.removeEventListener('error', onError);
    };
    video.addEventListener('seeked', onSeeked, { once: true });
    video.addEventListener('error', onError, { once: true });
    video.currentTime = Math.min(t, Math.max(0, video.duration - 0.05));
  });
}

function toBlob(canvas: HTMLCanvasElement, type: string, quality: number): Promise<Blob> {
  return new Promise((resolve, reject) => {
    canvas.toBlob(
      (b) => (b ? resolve(b) : reject(new Error('canvas.toBlob returned null'))),
      type,
      quality,
    );
  });
}
