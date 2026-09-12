import { ChangeDetectionStrategy, Component, input } from '@angular/core';
import { Job } from '../../models/job.model';

@Component({
  selector: 'vx-collage-view',
  standalone: true,
  changeDetection: ChangeDetectionStrategy.OnPush,
  styles: [
    `
      :host { display: block; }
      .grid { display: grid; grid-template-columns: 320px 1fr; gap: 16px; }
      @media (max-width: 780px) { .grid { grid-template-columns: 1fr; } }
      img { width: 100%; border-radius: 8px; border: 1px solid var(--line); display: block; }
      h3 { margin: 0 0 6px; }
      .story { white-space: pre-wrap; line-height: 1.5; }
      .muted { color: var(--muted); }
      .tag {
        font-size: 11px; padding: 1px 6px; border: 1px solid var(--line);
        border-radius: 999px; color: var(--muted);
      }
    `,
  ],
  template: `
    <div class="grid">
      <div>
        @if (job()?.overviewCollageUrl) {
          <img [src]="job()!.overviewCollageUrl" alt="macro collage" />
        } @else {
          <p class="muted">Building macro collage…</p>
        }
      </div>
      <div>
        <h3>
          Story
          @if (job()?.analysisProvider) {
            <span class="tag">{{ job()!.analysisProvider }}</span>
          }
        </h3>
        <p class="story">{{ job()?.overviewStory || '…' }}</p>

        <h3>Transcript</h3>
        @if (job()?.transcript) {
          <p class="story">{{ job()!.transcript }}</p>
        } @else if (job()?.status === 'complete' || job()?.status === 'error') {
          <p class="muted">No speech detected.</p>
        } @else {
          <p class="muted">{{ job()?.audioReceived ? 'Transcribing…' : 'Extracting audio…' }}</p>
        }
      </div>
    </div>
  `,
})
export class CollageViewComponent {
  readonly job = input<Job | null>(null);
}
