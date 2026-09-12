/** Formats a JobEvent's ISO timestamp as a local HH:MM:SS wall-clock time for
 *  the activity log. Invalid/missing input returns ''. */
export function formatEventTime(iso: string): string {
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return '';
  return d.toLocaleTimeString(undefined, { hour12: false });
}
