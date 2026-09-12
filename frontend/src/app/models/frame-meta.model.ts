/** One cell of the Stage-1 macro collage. */
export interface FrameMeta {
  t: number; // timestamp in seconds within the source video
  row: number;
  col: number;
  x: number; // pixel x of the cell top-left in the collage
  y: number;
  w: number;
  h: number;
  label?: string; // human timestamp stamped onto the frame (HH:MM:SS)
}

export interface OverviewMeta {
  cols: number;
  rows: number;
  cellWidth: number;
  cellHeight: number;
  frames: FrameMeta[];
  interestHints?: { segmentIndex: number; score: number }[];
}
