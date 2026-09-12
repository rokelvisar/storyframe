import { ChangeDetectionStrategy, Component, OnInit, inject, signal } from '@angular/core';
import { firstValueFrom } from 'rxjs';
import { ApiService } from '../../core/api.service';
import { formatJobDate } from '../../core/format-job-date';
import { JobStore } from '../../core/job-store';
import { JobSummary } from '../../models/job.model';

@Component({
  selector: 'vx-previous-jobs',
  standalone: true,
  changeDetection: ChangeDetectionStrategy.OnPush,
  styles: [
    `
      :host { display: block; }
      .head { display: flex; align-items: center; gap: 10px; margin-bottom: 8px; }
      h3 { margin: 0; }
      button.link { background: none; border: none; color: var(--accent); cursor: pointer; font: inherit; padding: 0; }
      .hint { color: var(--muted); font-size: 13px; }
      .grid {
        display: grid; grid-template-columns: repeat(auto-fill, minmax(160px, 1fr));
        gap: 10px; max-height: 420px; overflow-y: auto;
      }
      .card {
        display: flex; flex-direction: column; gap: 4px; cursor: pointer;
        border: 1px solid var(--line); border-radius: 8px; overflow: hidden;
        background: var(--panel-2); text-align: left; font: inherit; color: var(--text); padding: 0;
      }
      .card:hover { border-color: var(--accent); }
      .thumb {
        width: 100%; aspect-ratio: 16/9; object-fit: cover; background: #0003; display: block;
      }
      .thumb.placeholder { display: flex; align-items: center; justify-content: center; color: var(--muted); font-size: 11px; }
      .meta { padding: 6px 8px 8px; display: flex; flex-direction: column; gap: 2px; }
      .filename { font-size: 12px; font-weight: 600; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
      .subline { display: flex; gap: 6px; align-items: center; font-size: 11px; color: var(--muted); }
      .status-dot { width: 7px; height: 7px; border-radius: 50%; flex-shrink: 0; }
      .status-dot.complete { background: #4caf7d; }
      .status-dot.error { background: #e8586a; }
      .status-dot.other { background: #e8b339; }
    `,
  ],
  template: `
    <div class="head">
      <h3>Previous videos</h3>
      <button class="link" (click)="refresh()" [disabled]="loading()">
        {{ loading() ? 'Loading…' : 'Refresh' }}
      </button>
    </div>

    @if (!loading() && jobs().length === 0) {
      <p class="hint">No videos analyzed yet — pick a file or import from Immich above.</p>
    } @else {
      <div class="grid">
        @for (j of jobs(); track j.id) {
          <button class="card" (click)="open(j)" [title]="j.filename">
            @if (j.overviewCollageUrl) {
              <img class="thumb" [src]="j.overviewCollageUrl" [alt]="j.filename" />
            } @else {
              <div class="thumb placeholder">no preview</div>
            }
            <div class="meta">
              <span class="filename">{{ j.filename }}</span>
              <span class="subline">
                <span class="status-dot" [class]="dotClass(j.status)"></span>
                {{ j.origin === 'immich' ? 'Immich' : 'upload' }} · {{ formatDate(j.createdAt) }}
              </span>
            </div>
          </button>
        }
      </div>
    }
  `,
})
export class PreviousJobsComponent implements OnInit {
  private readonly api = inject(ApiService);
  private readonly store = inject(JobStore);

  readonly jobs = signal<JobSummary[]>([]);
  readonly loading = signal(false);

  ngOnInit(): void {
    void this.refresh();
  }

  async refresh(): Promise<void> {
    this.loading.set(true);
    try {
      const res = await firstValueFrom(this.api.listJobs());
      this.jobs.set(res.jobs);
    } finally {
      this.loading.set(false);
    }
  }

  open(j: JobSummary): void {
    void this.store.loadJob(j.id);
  }

  dotClass(status: JobSummary['status']): string {
    if (status === 'complete') return 'complete';
    if (status === 'error') return 'error';
    return 'other';
  }

  formatDate(iso: string): string {
    return formatJobDate(iso);
  }
}
