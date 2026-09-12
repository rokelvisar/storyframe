import { TimelineSegment } from './timeline-segment.model';
import { FrameMeta } from './frame-meta.model';

// Mirrors shared/openapi.yaml and backend/internal/models/models.go.

export type JobStatus =
  | 'created'
  | 'downloading' // origin=immich: backend is fetching the asset server-side
  | 'overview_received'
  | 'audio_received'
  | 'analyzing'
  | 'complete'
  | 'error';

/** How the source bytes reached the server. */
export type JobOrigin = 'upload' | 'immich';

export type ChatRole = 'user' | 'assistant';

export interface ChatMessage {
  role: ChatRole;
  content: string;
  createdAt: string;
}

export type EventLevel = 'info' | 'warn' | 'error';

/** One entry in a job's running activity log: pipeline steps ("building macro
 *  collage", "job complete") and AI-provider decisions ("VLM: trying
 *  gemini-...", "... failed, falling back"). */
export interface JobEvent {
  time: string;
  level: EventLevel;
  message: string;
}

export interface Job {
  id: string;
  filename: string;
  sizeBytes: number;
  durationSec: number;
  mimeType?: string;
  status: JobStatus;
  origin: JobOrigin;
  immichAssetId?: string;
  /** origin=immich only. True once the analysis has been written back onto
   *  the source asset (manually or automatically) at least once. */
  immichAutoWriteback?: boolean;
  immichWrittenBack?: boolean;
  createdAt: string;
  updatedAt: string;
  overviewCollageUrl?: string;
  overviewStory?: string;
  audioReceived: boolean;
  transcript?: string;
  analysisProvider?: 'noop' | 'litellm';
  error?: string;
  frames?: FrameMeta[];
  segments: TimelineSegment[];
  /** Unset for origin=immich jobs once complete — the raw video is deleted
   *  locally (the original stays in Immich); nothing to download. */
  archiveUrl?: string;
  chatMessages?: ChatMessage[];
  events?: JobEvent[];
  transcribeLanguageHint?: string;
  outputLanguage?: string;
}

/** Optional language overrides shared by both create-job request shapes.
 *  languageHint: ISO-639-1 code, or a comma-separated candidate list (e.g.
 *  "en,sl") to softly steer Whisper. outputLanguage: ISO-639-1 code the VLM
 *  should write the story/descriptions/chat replies in. Both fall back to the
 *  server defaults (TRANSCRIBE_LANGUAGE_HINT / OUTPUT_LANGUAGE) when omitted. */
export interface LanguageOptions {
  languageHint?: string;
  outputLanguage?: string;
}

export interface CreateJobRequest extends LanguageOptions {
  filename: string;
  sizeBytes: number;
  durationSec: number;
  mimeType?: string;
  segmentSeconds?: number;
  segmentPlan?: { startSec: number; endSec: number }[];
}

export interface CreateJobResponse {
  job: Job;
  /** Root-relative tus base URL; omitted for origin=immich jobs (no browser upload). */
  tusEndpoint?: string;
}

/** Lightweight projection of a Job for the "previous videos" timeline. */
export interface JobSummary {
  id: string;
  filename: string;
  status: JobStatus;
  origin: JobOrigin;
  durationSec: number;
  overviewCollageUrl?: string;
  overviewStory?: string;
  createdAt: string;
  updatedAt: string;
}

export interface ListJobsResponse {
  jobs: JobSummary[];
}

/** POST /jobs/from-immich body. Exactly one of assetId / shareLink is required;
 *  shareLink accepts a raw share key or a full https://host/share/<key> URL. */
export interface CreateJobFromImmichRequest extends LanguageOptions {
  assetId?: string;
  shareLink?: string;
  segmentSeconds?: number;
  /** Write the analysis back onto the source asset automatically on
   *  completion. Default false — use the "Write back to Immich" button. */
  autoWriteback?: boolean;
}

export interface ProcessSegmentRequest {
  source?: 'user' | 'ai' | 'plan';
  interestScore?: number;
  reason?: string;
}

export interface ChatRequest {
  message: string;
}
