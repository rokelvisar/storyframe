import { CreateJobFromImmichRequest } from '../models/job.model';

/**
 * A pasted value is either a full share URL (contains "://" or "/share/") or a
 * bare asset id / share key. The backend does the authoritative parsing either
 * way (immich.ExtractShareKey); this only decides which request field to send
 * the raw input in.
 */
export function parseImmichInput(input: string): CreateJobFromImmichRequest {
  const v = input.trim();
  if (v.includes('://') || v.includes('/share/')) return { shareLink: v };
  return { assetId: v };
}
