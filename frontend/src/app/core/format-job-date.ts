/** Formats a JobSummary's ISO createdAt as a local date+time for the
 *  "previous videos" timeline. Invalid/missing input returns ''. */
export function formatJobDate(iso: string): string {
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return '';
  return d.toLocaleString(undefined, { dateStyle: 'medium', timeStyle: 'short' });
}
