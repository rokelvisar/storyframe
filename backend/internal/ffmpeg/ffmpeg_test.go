package ffmpeg

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestSceneArgs_Golden(t *testing.T) {
	got := SceneArgs(GranularOptions{
		InputPath:  "/data/assembled/j1/source.mp4",
		OutputPath: "/data/collages/j1/seg-002.jpg",
		StartSec:   60,
		EndSec:     90,
	})
	joined := strings.Join(got, " ")

	for _, want := range []string{
		"-ss 60 -to 90",
		"-i /data/assembled/j1/source.mp4",
		"select='gt(scene\\,0.300)+eq(n\\,0)'",
		"showinfo",
		"scale=320:-2",
		"drawtext=fontfile=" + DefaultFont,
		"text='%{pts\\:hms\\:60}'",
		"tile=5x5:padding=3:margin=3:color=black",
		"-frames:v 1 -fps_mode vfr",
		"/data/collages/j1/seg-002.jpg",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("SceneArgs missing %q\n got: %s", want, joined)
		}
	}
}

func TestFixedRateArgs_UsesFps(t *testing.T) {
	got := strings.Join(FixedRateArgs(GranularOptions{
		InputPath: "in.mp4", OutputPath: "out.jpg", StartSec: 0, EndSec: 10, FPSFallback: 3,
	}), " ")
	if !strings.Contains(got, "fps=3,") {
		t.Fatalf("expected fps=3 filter, got: %s", got)
	}
	if strings.Contains(got, "select=") {
		t.Fatalf("fallback must not use scene select, got: %s", got)
	}
}

func TestConcatArgs(t *testing.T) {
	got := strings.Join(ConcatArgs("/tmp/list.txt", "/tmp/archive.mp4"), " ")
	if !strings.Contains(got, "-f concat -safe 0 -i /tmp/list.txt") || !strings.Contains(got, "-c copy /tmp/archive.mp4") {
		t.Fatalf("unexpected concat args: %s", got)
	}
}

func TestTrimFloat(t *testing.T) {
	for in, want := range map[float64]string{60: "60", 12.5: "12.5", 0: "0", 1.234: "1.234"} {
		if got := trimFloat(in); got != want {
			t.Errorf("trimFloat(%v) = %q, want %q", in, got, want)
		}
	}
}

func TestFfprobeBin(t *testing.T) {
	cases := map[string]string{
		"ffmpeg":          "ffprobe",
		"/usr/bin/ffmpeg": "/usr/bin/ffprobe",
		"":                "ffprobe",
		"/opt/custom-bin": "ffprobe", // no trailing "ffmpeg" -> safe fallback
	}
	for in, want := range cases {
		if got := ffprobeBin(in); got != want {
			t.Errorf("ffprobeBin(%q) = %q, want %q", in, got, want)
		}
	}
}

func requireBin(t *testing.T, bin string) {
	t.Helper()
	if _, err := exec.LookPath(bin); err != nil {
		t.Skipf("%s not on PATH", bin)
	}
}

// makeTestVideo renders a tiny lavfi test clip; skips the test if ffmpeg can't run it.
func makeTestVideo(t *testing.T, path string, w, h int, seconds int) {
	t.Helper()
	requireBin(t, "ffmpeg")
	cmd := exec.Command("ffmpeg", "-v", "error", "-y",
		"-f", "lavfi", "-i", fmt.Sprintf("testsrc2=s=%dx%d:d=%d", w, h, seconds),
		"-f", "lavfi", "-i", fmt.Sprintf("sine=f=440:d=%d", seconds),
		"-map", "0:v", "-map", "1:a",
		"-c:v", "libx264", "-pix_fmt", "yuv420p", "-c:a", "aac", "-movflags", "+faststart",
		path,
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg fixture: %v: %s", err, out)
	}
}

func TestProbeDimensions_RealFile(t *testing.T) {
	requireBin(t, "ffprobe")
	dir := t.TempDir()
	src := dir + "/in.mp4"
	makeTestVideo(t, src, 480, 270, 2)

	w, h, err := ProbeDimensions(context.Background(), "ffmpeg", src)
	if err != nil {
		t.Fatal(err)
	}
	if w != 480 || h != 270 {
		t.Fatalf("got %dx%d, want 480x270", w, h)
	}
}

func TestExtractAudio_RealFile(t *testing.T) {
	requireBin(t, "ffmpeg")
	dir := t.TempDir()
	src, out := dir+"/in.mp4", dir+"/audio.webm"
	makeTestVideo(t, src, 320, 240, 2)

	if err := ExtractAudio(context.Background(), "ffmpeg", src, out); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(out)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() == 0 {
		t.Fatal("extracted audio file is empty")
	}
}

func TestRunOverview_RealFile(t *testing.T) {
	requireBin(t, "ffmpeg")
	dir := t.TempDir()
	src, out := dir+"/in.mp4", dir+"/overview.jpg"
	makeTestVideo(t, src, 320, 240, 6)

	res, err := RunOverview(context.Background(), GranularOptions{
		InputPath: src, OutputPath: out,
		StartSec: 0, EndSec: 6,
		Cols: 3, Rows: 2, CellWidth: 160,
		FPSFallback: 1, // 6 frames over 6s
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.FrameCount == 0 {
		t.Fatal("expected at least one sampled frame")
	}
	if info, err := os.Stat(out); err != nil || info.Size() == 0 {
		t.Fatalf("overview collage missing or empty: %v", err)
	}
}
