import { Injectable } from '@angular/core';

export interface AudioTrackResult {
  blob: Blob;
  mimeType: string;
  durationSec: number;
}

/**
 * Stage 2: decodes the selected file's audio with a <video> element routed through
 * a Web Audio graph into a MediaRecorder, producing a small Opus/WebM blob
 * (~5-10 MB) instead of shipping the whole 2 GB file for transcription.
 *
 * MediaRecorder captures in real time, so an optional playbackRate > 1 trades a
 * little quality for wall-clock time. Default is 1x.
 */
@Injectable({ providedIn: 'root' })
export class AudioExtractorService {
  async extract(file: File, opts: { playbackRate?: number; bitsPerSecond?: number } = {}): Promise<AudioTrackResult> {
    const playbackRate = clampRate(opts.playbackRate ?? 1);
    const bps = opts.bitsPerSecond ?? 24_000;

    const url = URL.createObjectURL(file);
    const media = document.createElement('video');
    media.preload = 'auto';
    media.src = url;
    media.muted = true; // don't play out loud; we tap the graph instead

    const AudioCtx = window.AudioContext ?? (window as unknown as { webkitAudioContext: typeof AudioContext }).webkitAudioContext;
    const audioCtx = new AudioCtx();

    try {
      await once(media, 'loadedmetadata');
      const duration = media.duration;

      const source = audioCtx.createMediaElementSource(media);
      const dest = audioCtx.createMediaStreamDestination();
      const silentGain = audioCtx.createGain();
      silentGain.gain.value = 0;
      source.connect(dest); // -> recorder
      source.connect(silentGain).connect(audioCtx.destination); // keep the graph pulling

      const mimeType = pickMime();
      const recorder = new MediaRecorder(dest.stream, {
        mimeType,
        audioBitsPerSecond: bps,
      });
      const chunks: BlobPart[] = [];
      recorder.ondataavailable = (e) => e.data.size && chunks.push(e.data);

      const stopped = new Promise<void>((resolve) => (recorder.onstop = () => resolve()));

      media.playbackRate = playbackRate;
      recorder.start(1000);
      await audioCtx.resume();
      await media.play();
      await once(media, 'ended');
      recorder.stop();
      await stopped;

      return { blob: new Blob(chunks, { type: mimeType }), mimeType, durationSec: duration };
    } finally {
      media.removeAttribute('src');
      media.load();
      URL.revokeObjectURL(url);
      void audioCtx.close();
    }
  }
}

function pickMime(): string {
  const candidates = ['audio/webm;codecs=opus', 'audio/webm', 'audio/ogg;codecs=opus', 'audio/mp4'];
  for (const c of candidates) {
    if (typeof MediaRecorder !== 'undefined' && MediaRecorder.isTypeSupported(c)) return c;
  }
  return 'audio/webm';
}

function clampRate(r: number): number {
  return Math.min(8, Math.max(1, r));
}

function once(el: HTMLMediaElement, ev: string): Promise<void> {
  return new Promise((resolve, reject) => {
    const ok = () => {
      cleanup();
      resolve();
    };
    const bad = () => {
      cleanup();
      reject(new Error(`audio pipeline error while waiting for ${ev}`));
    };
    const cleanup = () => {
      el.removeEventListener(ev, ok);
      el.removeEventListener('error', bad);
    };
    el.addEventListener(ev, ok, { once: true });
    el.addEventListener('error', bad, { once: true });
  });
}
