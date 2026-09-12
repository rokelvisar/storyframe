import { DecimalPipe } from '@angular/common';
import { ChangeDetectionStrategy, Component, computed, input, output } from '@angular/core';
import { TimelineSegment } from '../../models/timeline-segment.model';
import { formatTimecode } from '../../core/time-util';

@Component({
  selector: 'vx-segment',
  standalone: true,
  imports: [DecimalPipe],
  changeDetection: ChangeDetectionStrategy.OnPush,
  styles: [
    `
      .seg {
        position: relative;
        border: 1px solid var(--line);
        border-radius: 6px;
        padding: 8px;
        min-height: 92px;
        display: flex;
        flex-direction: column;
        gap: 6px;
        cursor: pointer;
        overflow: hidden;
        background: var(--panel-2);
      }
      .seg:hover { border-color: var(--accent); }
      .seg.done { border-color: #2f7d4f; }
      .seg.error { border-color: #b5423a; }
      .heat {
        position: absolute;
        inset: 0;
        pointer-events: none;
        opacity: 0.22;
      }
      .row { display: flex; justify-content: space-between; font-size: 12px; color: var(--muted); }
      .badge {
        align-self: flex-start;
        font-size: 11px;
        padding: 1px 6px;
        border-radius: 999px;
        border: 1px solid var(--line);
        background: rgba(0, 0, 0, 0.25);
      }
      .bar { height: 4px; border-radius: 2px; background: #0c1013; overflow: hidden; }
      .bar > i { display: block; height: 100%; background: var(--accent); }
      .thumb { width: 100%; border-radius: 4px; display: block; }
      .desc { font-size: 12px; color: var(--text); line-height: 1.35; }
    `,
  ],
  template: `
    <div
      class="seg"
      [class.done]="seg().status === 'done'"
      [class.error]="seg().status === 'error'"
      (click)="pick.emit(seg())"
      [title]="tooltip()"
    >
      <div class="heat" [style.background]="heat()"></div>
      <div class="row">
        <span>#{{ seg().index }} · {{ tc(seg().startSec) }}–{{ tc(seg().endSec) }}</span>
        <span>{{ (seg().interestScore * 100) | number: '1.0-0' }}%</span>
      </div>
      <span class="badge">{{ label() }}</span>

      @if (seg().status === 'uploading' || seg().status === 'uploaded') {
        <div class="bar"><i [style.width.%]="uploadPct()"></i></div>
      }

      @if (seg().granularCollageUrl) {
        <img class="thumb" [src]="seg().granularCollageUrl" alt="granular collage for segment {{ seg().index }}" loading="lazy" />
      }
      @if (seg().description) {
        <div class="desc">{{ seg().description }}</div>
      }
    </div>
  `,
})
export class SegmentComponent {
  readonly seg = input.required<TimelineSegment>();
  readonly pick = output<TimelineSegment>();

  readonly tc = formatTimecode;

  readonly uploadPct = computed(() => {
    const s = this.seg();
    return s.totalBytes > 0 ? Math.min(100, (s.uploadedBytes / s.totalBytes) * 100) : 0;
  });

  readonly heat = computed(() => {
    const v = Math.max(0, Math.min(1, this.seg().interestScore));
    // blue (cool) -> orange (hot)
    const hue = 210 - v * 180;
    return `linear-gradient(180deg, hsl(${hue} 90% 55%), transparent)`;
  });

  readonly label = computed(() => {
    const s = this.seg();
    switch (s.status) {
      case 'pending':
        return 'queued';
      case 'uploading':
        return `up ${this.uploadPct().toFixed(0)}%`;
      case 'uploaded':
        return 'uploaded';
      case 'processing':
        return 'analysing…';
      case 'done':
        return `done · ${s.source}`;
      case 'error':
        return 'error';
    }
  });

  readonly tooltip = computed(() => {
    const s = this.seg();
    const parts = [`source: ${s.source}`, `status: ${s.status}`];
    if (s.sceneChangeTimes?.length) parts.push(`${s.sceneChangeTimes.length} scene cut(s)`);
    if (s.error) parts.push(`error: ${s.error}`);
    return parts.join(' · ');
  });
}
