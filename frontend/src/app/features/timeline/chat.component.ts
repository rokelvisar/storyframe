import { ChangeDetectionStrategy, Component, computed, inject, signal } from '@angular/core';
import { JobStore } from '../../core/job-store';

@Component({
  selector: 'vx-chat',
  standalone: true,
  changeDetection: ChangeDetectionStrategy.OnPush,
  styles: [
    `
      :host { display: block; }
      h3 { margin: 0 0 8px; }
      .hint { color: var(--muted); font-size: 13px; }
      .messages {
        display: flex; flex-direction: column; gap: 8px;
        max-height: 320px; overflow-y: auto; padding-right: 4px; margin-bottom: 10px;
      }
      .msg { max-width: 80%; padding: 7px 10px; border-radius: 10px; font-size: 13px; line-height: 1.4; white-space: pre-wrap; }
      .msg.user { align-self: flex-end; background: var(--accent); color: #04101f; }
      .msg.assistant { align-self: flex-start; background: var(--panel-2); border: 1px solid var(--line); }
      .thinking { align-self: flex-start; color: var(--muted); font-size: 12px; font-style: italic; }
      .row { display: flex; gap: 8px; }
      input[type='text'] {
        flex: 1; background: var(--panel-2); border: 1px solid var(--line); border-radius: 6px;
        color: var(--text); padding: 6px 9px; font: inherit;
      }
    `,
  ],
  template: `
    <h3>Ask about this video</h3>

    @if (!ready()) {
      <p class="hint">Chat opens once the overview story is ready.</p>
    } @else {
      @if (store.chatMessages().length) {
        <div class="messages">
          @for (m of store.chatMessages(); track $index) {
            <div class="msg" [class.user]="m.role === 'user'" [class.assistant]="m.role === 'assistant'">
              {{ m.content }}
            </div>
          }
          @if (waitingForReply()) {
            <div class="thinking">thinking…</div>
          }
        </div>
      } @else {
        <p class="hint">Ask anything — "what happens around the middle?", "is there any dialogue?", "summarize segment 2".</p>
      }

      <div class="row">
        <input
          #box
          type="text"
          placeholder="Ask a question…"
          [disabled]="sending()"
          (keydown.enter)="send(box)"
        />
        <button [disabled]="sending() || !box.value.trim()" (click)="send(box)">Send</button>
      </div>
    }
  `,
})
export class ChatComponent {
  readonly store = inject(JobStore);
  readonly sending = signal(false);

  readonly ready = computed(() => !!this.store.job()?.overviewStory);
  /** Odd message count = the last turn is a user question with no reply yet. */
  readonly waitingForReply = computed(() => this.store.chatMessages().length % 2 === 1);

  async send(box: HTMLInputElement): Promise<void> {
    const text = box.value.trim();
    if (!text) return;
    box.value = '';
    this.sending.set(true);
    try {
      await this.store.sendChatMessage(text);
    } finally {
      this.sending.set(false);
    }
  }
}
