import { ChangeDetectionStrategy, Component, inject, signal } from '@angular/core';
import { JobStore } from './core/job-store';
import { LanguageOptions } from './models/job.model';
import { ChatComponent } from './features/timeline/chat.component';
import { EventLogComponent } from './features/timeline/event-log.component';
import { CollageViewComponent } from './features/timeline/collage-view.component';
import { TimelineComponent } from './features/timeline/timeline.component';
import { PreviousJobsComponent } from './features/history/previous-jobs.component';

@Component({
  selector: 'vx-root',
  standalone: true,
  imports: [ChatComponent, CollageViewComponent, EventLogComponent, PreviousJobsComponent, TimelineComponent],
  changeDetection: ChangeDetectionStrategy.OnPush,
  styles: [
    `
      .wrap { max-width: 1200px; margin: 0 auto; padding: 24px 20px 64px; display: flex; flex-direction: column; gap: 20px; }
      header { display: flex; align-items: baseline; gap: 12px; flex-wrap: wrap; }
      header h1 { margin: 0; font-size: 20px; }
      header .sub { color: var(--muted); font-size: 13px; }
      .picker { display: flex; align-items: center; gap: 12px; flex-wrap: wrap; }
      .status { font-size: 13px; color: var(--muted); }
      .err { color: #ff8a80; }
      input[type='file'] { color: var(--muted); }
      .phasechip {
        font-size: 12px; padding: 2px 8px; border-radius: 999px;
        border: 1px solid var(--line); text-transform: capitalize;
      }
      .or { color: var(--muted); font-size: 12px; }
      .immich-input {
        background: var(--panel-2); border: 1px solid var(--line); border-radius: 6px;
        color: var(--text); padding: 5px 8px; min-width: 280px; font: inherit;
      }
      .origin-badge {
        font-size: 11px; padding: 1px 7px; border-radius: 999px;
        border: 1px solid var(--line); color: var(--muted);
      }
      .lang-row { display: flex; align-items: center; gap: 6px; flex-wrap: wrap; width: 100%; }
      .lang-row label { font-size: 12px; color: var(--muted); }
      .writeback-row { display: flex; align-items: center; gap: 6px; }
      .writeback-row label { font-size: 12px; color: var(--muted); cursor: pointer; }
      select {
        background: var(--panel-2); border: 1px solid var(--line); border-radius: 6px;
        color: var(--text); padding: 4px 6px; font: inherit;
      }
    `,
  ],
  template: `
    <div class="wrap">
      <header>
        <h1>Video Extractor</h1>
        <span class="sub">3-stage hybrid coarse-to-fine VLM storytelling</span>
      </header>

      <section class="panel picker">
        <input
          type="file"
          accept="video/*"
          [disabled]="busy()"
          (change)="onFile($event, langHint.value, outLang.value)"
        />
        <span class="or">or</span>
        <input
          #immichInput
          type="text"
          class="immich-input"
          placeholder="Immich asset ID or share link"
          [disabled]="busy()"
          (keydown.enter)="onImmich(immichInput.value, langHint.value, outLang.value, autoWriteback.checked)"
        />
        <button
          [disabled]="busy() || !immichInput.value"
          (click)="onImmich(immichInput.value, langHint.value, outLang.value, autoWriteback.checked)"
        >
          Import from Immich
        </button>
        <span class="writeback-row">
          <input #autoWriteback type="checkbox" id="autoWriteback" [disabled]="busy()" />
          <label for="autoWriteback">Auto write-back to Immich</label>
        </span>
        <span class="phasechip">{{ store.phase() }}</span>
        @if (store.audioPhase() !== 'idle') {
          <span class="phasechip">audio: {{ store.audioPhase() }}</span>
        }
        @if (store.error(); as e) {
          <span class="status err">{{ e }}</span>
        }

        <div class="lang-row">
          <label for="langHint">Transcribe:</label>
          <select #langHint id="langHint" [disabled]="busy()">
            <option value="">Auto (English/Slovenian)</option>
            <option value="en">English only</option>
            <option value="sl">Slovenian only</option>
          </select>
          <label for="outLang">Reply in:</label>
          <select #outLang id="outLang" [disabled]="busy()">
            <option value="sl" selected>Slovenian</option>
            <option value="en">English</option>
          </select>
        </div>
      </section>

      <section class="panel">
        <vx-previous-jobs />
      </section>

      @if (store.job(); as job) {
        <section class="panel">
          <div class="row" style="display:flex; align-items:center; gap:8px; margin-bottom:8px; flex-wrap:wrap;">
            <strong>{{ job.filename }}</strong>
            <span class="origin-badge">{{ job.origin === 'immich' ? 'from Immich' : 'uploaded' }}</span>
            @if (job.archiveUrl) {
              <a class="status" [href]="job.archiveUrl" target="_blank" rel="noopener">Download archive</a>
            } @else if (job.origin === 'immich' && job.status === 'complete') {
              <span class="status">Original in Immich (asset {{ job.immichAssetId }})</span>
            }
            @if (job.origin === 'immich' && job.status === 'complete') {
              <button [disabled]="writingBack()" (click)="writeback()">
                {{ job.immichWrittenBack ? 'Re-write to Immich' : 'Write back to Immich' }}
              </button>
            }
          </div>
          <vx-collage-view [job]="job" />
        </section>

        <section class="panel">
          <vx-timeline />
        </section>

        <section class="panel">
          <vx-chat />
        </section>

        <section class="panel">
          <vx-event-log />
        </section>
      } @else {
        <p class="status">
          Pick a local video, or paste an Immich asset ID / share link. A local file
          builds a macro collage + compressed audio in the browser (~2&nbsp;s) then
          uploads segments, interesting parts first; an Immich import is fetched and
          processed entirely server-side.
        </p>
      }
    </div>
  `,
})
export class AppComponent {
  readonly store = inject(JobStore);
  readonly writingBack = signal(false);

  busy(): boolean {
    const p = this.store.phase();
    return p === 'reading' || p === 'overview' || p === 'importing';
  }

  onFile(ev: Event, languageHint: string, outputLanguage: string): void {
    const input = ev.target as HTMLInputElement;
    const file = input.files?.[0];
    if (file) void this.store.run(file, toLanguageOptions(languageHint, outputLanguage));
  }

  onImmich(value: string, languageHint: string, outputLanguage: string, autoWriteback: boolean): void {
    const v = value.trim();
    if (v) void this.store.runFromImmich(v, toLanguageOptions(languageHint, outputLanguage), autoWriteback);
  }

  async writeback(): Promise<void> {
    this.writingBack.set(true);
    try {
      await this.store.triggerImmichWriteback();
    } finally {
      this.writingBack.set(false);
    }
  }
}

function toLanguageOptions(languageHint: string, outputLanguage: string): LanguageOptions {
  return { languageHint: languageHint || undefined, outputLanguage: outputLanguage || undefined };
}
