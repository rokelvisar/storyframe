import { HttpClient } from '@angular/common/http';
import { Injectable, inject } from '@angular/core';
import { Observable } from 'rxjs';
import { environment } from '../../environments/environment';
import {
  ChatRequest,
  CreateJobFromImmichRequest,
  CreateJobRequest,
  CreateJobResponse,
  Job,
  ListJobsResponse,
  ProcessSegmentRequest,
} from '../models/job.model';
import { OverviewMeta } from '../models/frame-meta.model';
import { TimelineSegment } from '../models/timeline-segment.model';

@Injectable({ providedIn: 'root' })
export class ApiService {
  private http = inject(HttpClient);
  private base = `${environment.apiBase}/api/v1`;

  createJob(req: CreateJobRequest): Observable<CreateJobResponse> {
    return this.http.post<CreateJobResponse>(`${this.base}/jobs`, req);
  }

  createJobFromImmich(req: CreateJobFromImmichRequest): Observable<CreateJobResponse> {
    return this.http.post<CreateJobResponse>(`${this.base}/jobs/from-immich`, req);
  }

  getJob(id: string): Observable<Job> {
    return this.http.get<Job>(`${this.base}/jobs/${id}`);
  }

  listJobs(limit = 100): Observable<ListJobsResponse> {
    return this.http.get<ListJobsResponse>(`${this.base}/jobs`, { params: { limit } });
  }

  postOverview(id: string, collage: Blob, meta: OverviewMeta): Observable<Job> {
    const fd = new FormData();
    fd.append('collage', collage, 'collage.jpg');
    fd.append('meta', JSON.stringify(meta));
    return this.http.post<Job>(`${this.base}/jobs/${id}/overview`, fd);
  }

  postAudio(id: string, audio: Blob): Observable<Job> {
    const fd = new FormData();
    fd.append('audio', audio, 'audio.webm');
    return this.http.post<Job>(`${this.base}/jobs/${id}/audio`, fd);
  }

  processSegment(id: string, segmentId: string, body: ProcessSegmentRequest = {}): Observable<TimelineSegment> {
    return this.http.post<TimelineSegment>(`${this.base}/jobs/${id}/segments/${segmentId}/process`, body);
  }

  finalize(id: string): Observable<Job> {
    return this.http.post<Job>(`${this.base}/jobs/${id}/finalize`, {});
  }

  postChat(id: string, message: string): Observable<Job> {
    return this.http.post<Job>(`${this.base}/jobs/${id}/chat`, { message } satisfies ChatRequest);
  }

  postImmichWriteback(id: string): Observable<Job> {
    return this.http.post<Job>(`${this.base}/jobs/${id}/immich-writeback`, {});
  }
}
