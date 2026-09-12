package engine

import (
	"testing"

	"github.com/rokelvisar/storyframe/backend/internal/immich"
)

func TestPlanOverviewFrames_GridAndTimestamps(t *testing.T) {
	meta, fps := planOverviewFrames(100, 20, 320, 16.0/9.0)

	if len(meta.Frames) != 20 {
		t.Fatalf("want 20 frames, got %d", len(meta.Frames))
	}
	if meta.Cols != 5 || meta.Rows != 4 {
		t.Fatalf("want a 5x4 grid, got %dx%d", meta.Cols, meta.Rows)
	}
	if meta.CellWidth != 320 {
		t.Fatalf("cellWidth: got %d", meta.CellWidth)
	}
	wantFPS := 20.0 / 100.0
	if fps != wantFPS {
		t.Fatalf("fps: want %v, got %v", wantFPS, fps)
	}

	// evenly spaced, starting at t=0
	if meta.Frames[0].T != 0 {
		t.Fatalf("first frame should be at t=0, got %v", meta.Frames[0].T)
	}
	step := meta.Frames[1].T - meta.Frames[0].T
	for i := 1; i < len(meta.Frames); i++ {
		got := meta.Frames[i].T - meta.Frames[i-1].T
		if got != step {
			t.Fatalf("spacing not uniform at frame %d: %v vs step %v", i, got, step)
		}
	}

	// grid geometry: frame 5 -> row 1, col 0
	f5 := meta.Frames[5]
	if f5.Row != 1 || f5.Col != 0 || f5.X != 0 || f5.Y != meta.CellHeight {
		t.Fatalf("unexpected geometry for frame 5: %+v", f5)
	}
}

func TestPlanOverviewFrames_DefaultsOnBadInput(t *testing.T) {
	meta, fps := planOverviewFrames(0, 20, 320, 0)
	if len(meta.Frames) != 20 || fps <= 0 {
		t.Fatalf("expected safe defaults for zero duration/aspect, got meta=%+v fps=%v", meta, fps)
	}
}

func TestFormatTimecode(t *testing.T) {
	cases := map[float64]string{0: "00:00:00", 9: "00:00:09", 75: "00:01:15", 3661: "01:01:01", -5: "00:00:00"}
	for in, want := range cases {
		if got := formatTimecode(in); got != want {
			t.Errorf("formatTimecode(%v) = %q, want %q", in, got, want)
		}
	}
}

func TestExtForAsset(t *testing.T) {
	cases := []struct {
		a    immich.Asset
		want string
	}{
		{immich.Asset{OriginalFileName: "IMG_9381.MOV"}, ".mov"},
		{immich.Asset{OriginalFileName: "clip.mp4"}, ".mp4"},
		{immich.Asset{OriginalFileName: "", OriginalMimeType: "video/quicktime"}, ".mov"},
		{immich.Asset{OriginalFileName: "", OriginalMimeType: "video/mp4"}, ".mp4"},
		{immich.Asset{OriginalFileName: "", OriginalMimeType: "video/x-mystery"}, ".mp4"},
	}
	for _, c := range cases {
		if got := extForAsset(c.a); got != c.want {
			t.Errorf("extForAsset(%+v) = %q, want %q", c.a, got, c.want)
		}
	}
}
