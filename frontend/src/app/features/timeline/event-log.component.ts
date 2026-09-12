import { AfterViewChecked, ChangeDetectionStrategy, Component, ElementRef, inject, viewChild } from '@angular/core';
import { formatEventTime } from '../../core/format-event-time';
import { JobStore } from '../../core/job-store';

@Component({
  selector: 'vx-event-log',
  standalone: true,
  changeDetection: ChangeDetectionStrategy.OnPush,
  styles: [
    `
      :host { display: block; }
      h3 { margin: 0 0 8px; }
      .hint { color: var(--muted); font-size: 13px; }
      .log {
        display: flex; flex-direction: column; gap: 2px;
        max-height: 260px; overflow-y: auto;
        font-family: ui-monospace, 'SF Mono', Menlo, Consolas, monospace;
        font-size: 12px; line-height: 1.5;
      }
      .entry { display: flex; gap: 8px; padding: 1px 0; }
      .time { color: var(--muted); flex-shrink: 0; }
      .msg { white-space: pre-wrap; word-break: break-word; }
      .entry.info .msg { color: var(--text); }
      .entry.warn .msg { color: #e8b339; }
      .entry.error .msg { color: #e8586a; }
    `,
  ],
  template: `
    <h3>Activity log</h3>

    @if (!store.events().length) {
      <p class="hint">Pipeline steps and model activity will appear here once processing starts.</p>
    } @else {
      <div class="log" #scrollBox>
        @for (ev of store.events(); track $index) {
          <div class="entry" [class]="ev.level">
            <span class="time">{{ formatTime(ev.time) }}</span>
            <span class="msg">{{ ev.message }}</span>
          </div>
        }
      </div>
    }
  `,
})
export class EventLogComponent implements AfterViewChecked {
  readonly store = inject(JobStore);
  private readonly scrollBox = viewChild<ElementRef<HTMLDivElement>>('scrollBox');
  private lastCount = 0;

  ngAfterViewChecked(): void {
    const count = this.store.events().length;
    if (count === this.lastCount) return;
    this.lastCount = count;
    const el = this.scrollBox()?.nativeElement;
    if (el) el.scrollTop = el.scrollHeight;
  }

  formatTime(iso: string): string {
    return formatEventTime(iso);
  }
}
