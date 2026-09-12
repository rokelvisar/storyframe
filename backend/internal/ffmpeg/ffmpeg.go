// Package ffmpeg builds and runs the FFmpeg command lines for Stage-3 granular
// analysis: cut one timeline slice, detect scene changes, stamp each frame with its
// timestamp and tile the result into a single contact-sheet JPEG.
package ffmpeg

import (
	"bufio"
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
)

// DefaultFont is present in the runtime image via the fonts-dejavu-core package.
const DefaultFont = "/usr/share/fonts/truetype/dejavu/DejaVuSans-Bold.ttf"

// GranularOptions describes one per-segment contact sheet.
type GranularOptions struct {
	InputPath      string  // source video (a segment file or the full archive)
	OutputPath     string  // .jpg to write
	StartSec       float64 // absolute start within InputPath
	EndSec         float64 // absolute end
	Cols           int     // tile columns
	Rows           int     // tile rows
	CellWidth      int     // scaled cell width in px (height keeps aspect)
	SceneThreshold float64 // e.g. 0.3
	FPSFallback    float64 // e.g. 2 -> used when scene detection yields too few frames
	FontFile       string  // defaults to DefaultFont
	Bin            string  // ffmpeg binary, defaults to "ffmpeg"
}

func (o *GranularOptions) withDefaults() {
	if o.Cols == 0 {
		o.Cols = 5
	}
	if o.Rows == 0 {
		o.Rows = 5
	}
	if o.CellWidth == 0 {
		o.CellWidth = 320
	}
	if o.SceneThreshold == 0 {
		o.SceneThreshold = 0.3
	}
	if o.FPSFallback == 0 {
		o.FPSFallback = 2
	}
	if o.FontFile == "" {
		o.FontFile = DefaultFont
	}
	if o.Bin == "" {
		o.Bin = "ffmpeg"
	}
}

// drawtext stamps the absolute timestamp. The colon in the %{pts} expression must
// be escaped for FFmpeg's option parser; StartSec is added back as basetime so the
// label reflects the real position in the source video.
func (o GranularOptions) drawtext() string {
	return fmt.Sprintf(
		"drawtext=fontfile=%s:text='%%{pts\\:hms\\:%d}':x=6:y=6:fontsize=16:fontcolor=white:box=1:boxcolor=black@0.5",
		o.FontFile, int64(o.StartSec),
	)
}

func (o GranularOptions) tail() string {
	return fmt.Sprintf(
		"showinfo,scale=%d:-2,%s,tile=%dx%d:padding=3:margin=3:color=black",
		o.CellWidth, o.drawtext(), o.Cols, o.Rows,
	)
}

// SceneArgs is the primary command: select scene-change frames (plus the first).
func SceneArgs(o GranularOptions) []string {
	o.withDefaults()
	vf := fmt.Sprintf("select='gt(scene\\,%.3f)+eq(n\\,0)',%s", o.SceneThreshold, o.tail())
	return []string{
		"-hide_banner", "-y",
		"-ss", trimFloat(o.StartSec), "-to", trimFloat(o.EndSec),
		"-i", o.InputPath,
		"-an", "-sn",
		"-vf", vf,
		"-frames:v", "1", "-fps_mode", "vfr",
		"-q:v", "4",
		o.OutputPath,
	}
}

// FixedRateArgs is the fallback command: sample at a fixed FPS.
func FixedRateArgs(o GranularOptions) []string {
	o.withDefaults()
	vf := fmt.Sprintf("fps=%s,%s", trimFloat(o.FPSFallback), o.tail())
	return []string{
		"-hide_banner", "-y",
		"-ss", trimFloat(o.StartSec), "-to", trimFloat(o.EndSec),
		"-i", o.InputPath,
		"-an", "-sn",
		"-vf", vf,
		"-frames:v", "1", "-fps_mode", "vfr",
		"-q:v", "4",
		o.OutputPath,
	}
}

// ConcatArgs stitches ordered part files into a single mp4 without re-encoding.
// listPath is an ffmpeg concat-demuxer list file.
func ConcatArgs(listPath, outPath string) []string {
	return []string{
		"-hide_banner", "-y",
		"-f", "concat", "-safe", "0",
		"-i", listPath,
		"-c", "copy",
		outPath,
	}
}

// Result carries what we learned from a granular run.
type Result struct {
	SceneTimes []float64 // absolute seconds of selected frames
	FrameCount int
	UsedFallback bool
}

var ptsRe = regexp.MustCompile(`pts_time:([0-9.]+)`)

// RunGranular runs the scene-detect command; if it selects fewer than minFrames it
// retries at a fixed frame rate. It parses showinfo output for frame timestamps.
func RunGranular(ctx context.Context, o GranularOptions, minFrames int) (Result, error) {
	o.withDefaults()
	if minFrames <= 0 {
		minFrames = 4
	}

	times, err := runAndParse(ctx, o.Bin, SceneArgs(o), o.StartSec)
	if err != nil {
		return Result{}, err
	}
	if len(times) >= minFrames {
		return Result{SceneTimes: times, FrameCount: len(times)}, nil
	}

	fbTimes, err := runAndParse(ctx, o.Bin, FixedRateArgs(o), o.StartSec)
	if err != nil {
		// keep the scene-detect sheet we already produced
		return Result{SceneTimes: times, FrameCount: len(times)}, nil
	}
	return Result{SceneTimes: times, FrameCount: len(fbTimes), UsedFallback: true}, nil
}

// RunConcat executes the concat-demuxer command.
func RunConcat(ctx context.Context, bin, listPath, outPath string) error {
	if bin == "" {
		bin = "ffmpeg"
	}
	cmd := exec.CommandContext(ctx, bin, ConcatArgs(listPath, outPath)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("ffmpeg concat: %w: %s", err, tailStr(string(out), 500))
	}
	return nil
}

func runAndParse(ctx context.Context, bin string, args []string, base float64) ([]float64, error) {
	cmd := exec.CommandContext(ctx, bin, args...)
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	var times []float64
	var lastLines []string
	sc := bufio.NewScanner(stderr)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		lastLines = append(lastLines, line)
		if len(lastLines) > 20 {
			lastLines = lastLines[1:]
		}
		if m := ptsRe.FindStringSubmatch(line); m != nil {
			if v, e := strconv.ParseFloat(m[1], 64); e == nil {
				times = append(times, base+v)
			}
		}
	}
	if err := cmd.Wait(); err != nil {
		return nil, fmt.Errorf("ffmpeg: %w: %s", err, strings.Join(lastLines, " | "))
	}
	return times, nil
}

// RunOverview builds a single fixed-rate contact sheet across [StartSec,EndSec]
// (normally the whole video) — used for the Stage-1 macro collage when the
// backend already has the full file locally (e.g. an Immich import) instead of
// the browser building it. No scene-detect fallback: a fixed frame rate keeps
// the grid size predictable regardless of how eventful the source is.
func RunOverview(ctx context.Context, o GranularOptions) (Result, error) {
	o.withDefaults()
	times, err := runAndParse(ctx, o.Bin, FixedRateArgs(o), o.StartSec)
	if err != nil {
		return Result{}, err
	}
	return Result{SceneTimes: times, FrameCount: len(times), UsedFallback: true}, nil
}

// ExtractAudio pulls the audio track out of a local file into an Opus/WebM
// file, mirroring what the browser's audio-extractor.service.ts produces but
// running at whatever speed ffmpeg can manage instead of real time.
func ExtractAudio(ctx context.Context, bin, inputPath, outputPath string) error {
	if bin == "" {
		bin = "ffmpeg"
	}
	cmd := exec.CommandContext(ctx, bin,
		"-hide_banner", "-y", "-i", inputPath,
		"-vn", "-c:a", "libopus", "-b:a", "48k",
		outputPath,
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("ffmpeg extract audio: %w: %s", err, tailStr(string(out), 500))
	}
	return nil
}

// ProbeDimensions returns the decoded (post-rotation) width/height of the first
// video stream, used to size the macro-collage grid cells for the source's
// real aspect ratio (portrait phone video vs. 16:9, etc). ffprobeBin defaults
// to swapping a trailing "ffmpeg" for "ffprobe" in ffmpegBin, since the two
// ship together in every distro package this app targets.
func ProbeDimensions(ctx context.Context, ffmpegBin, path string) (width, height int, err error) {
	bin := ffprobeBin(ffmpegBin)
	cmd := exec.CommandContext(ctx, bin,
		"-v", "error", "-select_streams", "v:0",
		"-show_entries", "stream=width,height", "-of", "csv=s=x:p=0",
		path,
	)
	out, err := cmd.Output()
	if err != nil {
		return 0, 0, fmt.Errorf("ffprobe dimensions: %w", err)
	}
	parts := strings.SplitN(strings.TrimSpace(string(out)), "x", 2)
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("ffprobe dimensions: unexpected output %q", out)
	}
	w, err1 := strconv.Atoi(parts[0])
	h, err2 := strconv.Atoi(parts[1])
	if err1 != nil || err2 != nil || w <= 0 || h <= 0 {
		return 0, 0, fmt.Errorf("ffprobe dimensions: could not parse %q", out)
	}
	return w, h, nil
}

func ffprobeBin(ffmpegBin string) string {
	if ffmpegBin == "" {
		return "ffprobe"
	}
	if strings.HasSuffix(ffmpegBin, "ffmpeg") {
		return strings.TrimSuffix(ffmpegBin, "ffmpeg") + "ffprobe"
	}
	return "ffprobe"
}

func trimFloat(f float64) string {
	return strconv.FormatFloat(f, 'f', -1, 64)
}

func tailStr(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}
