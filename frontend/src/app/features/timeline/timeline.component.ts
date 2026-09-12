import { ChangeDetectionStrategy, Component, computed, inject } from '@angular/core';
import { JobStore } from '../../core/job-store';
import { TimelineSegment } from '../../models/timeline-segment.model';
import { formatTimecode } from '../../core/time-util';
import { SegmentComponent } from './segment.component';

@Component({
  selector: 'vx-timeline',
  standalone: true,
  imports: [SegmentComponent],
  changeDetection: ChangeDetectionStrategy.OnPush,
  styles: [
    `
      :host { display: block; }
      .ruler {
        display: flex;
        justify-content: space-between;
        font-size: 11px;
        color: var(--muted);
        border-bottom: 1px solid var(--line);
        padding-bottom: 4px;
        margin-bottom: 10px;
      }
      .segments {
        display: grid;
        grid-template-columns: repeat(auto-fill, minmax(200px, 1fr));
        gap: 10px;
      }
      .hint { color: var(--muted); font-size: 13px; margin-top: 8px; }
    `,
  ],
  template: `
    <div class="ruler">
      <span>00:00:00</span>
      <span>{{ mid() }}</span>
      <span>{{ end() }}</span>
    </div>

    <div class="segments">
      @for (seg of store.segments(); track seg.id) {
        <vx-segment [seg]="seg" (pick)="onPick($event)" />
      }
    </div>

    <p class="hint">
      Click a segment to prioritise its upload and request granular VLM analysis.
      Segments the model scores ≥ 60% are queued automatically.
    </p>
  `,
})
export class TimelineComponent {
  readonly store = inject(JobStore);

  private duration = computed(() => this.store.job()?.durationSec ?? 0);
  readonly mid = computed(() => formatTimecode(this.duration() / 2));
  readonly end = computed(() => formatTimecode(this.duration()));

  onPick(seg: TimelineSegment): void {
    if (seg.status === 'done' || seg.status === 'processing') return;
    void this.store.markInteresting(seg);
  }
}
