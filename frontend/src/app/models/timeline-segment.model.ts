export type SegmentStatus =
  | 'pending'
  | 'uploading'
  | 'uploaded'
  | 'processing'
  | 'done'
  | 'error';

export type SegmentSource = 'plan' | 'user' | 'ai';

export interface TimelineSegment {
  id: string;
  jobId: string;
  index: number;
  startSec: number;
  endSec: number;
  interestScore: number;
  source: SegmentSource;
  status: SegmentStatus;
  uploadedBytes: number;
  totalBytes: number;
  byteStart: number;
  byteEnd: number;
  granularCollageUrl?: string;
  sceneChangeTimes?: number[];
  description?: string;
  error?: string;
  updatedAt: string;
}
