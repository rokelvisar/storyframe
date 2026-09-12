package engine

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rokelvisar/storyframe/backend/internal/models"
	"github.com/rokelvisar/storyframe/backend/internal/store"
)

func TestContiguousBytes(t *testing.T) {
	e := &Engine{parts: map[string]map[int]partInfo{}}
	e.parts["j"] = map[int]partInfo{
		0: {index: 0, size: 100},
		1: {index: 1, size: 50},
		// gap at 2
		3: {index: 3, size: 999},
	}
	if got := e.contiguousBytes("j"); got != 150 {
		t.Fatalf("want 150 contiguous bytes (stop at the gap), got %d", got)
	}
	if got := e.contiguousBytes("unknown"); got != 0 {
		t.Fatalf("want 0 for unknown job, got %d", got)
	}
}

func TestConcatParts_OrdersByIndex(t *testing.T) {
	dir := t.TempDir()
	mk := func(name, content string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	e := &Engine{parts: map[string]map[int]partInfo{}}
	e.parts["j"] = map[int]partInfo{
		2: {index: 2, path: mk("2", "CCC"), size: 3},
		0: {index: 0, path: mk("0", "A"), size: 1},
		1: {index: 1, path: mk("1", "BB"), size: 2},
	}
	out := filepath.Join(dir, "joined.bin")
	if err := e.concatParts("j", out, 3); err != nil {
		t.Fatalf("concat: %v", err)
	}
	got, _ := os.ReadFile(out)
	if string(got) != "ABBCCC" {
		t.Fatalf("want ABBCCC, got %q", got)
	}
}

func TestConcatParts_MissingPart(t *testing.T) {
	e := &Engine{parts: map[string]map[int]partInfo{"j": {0: {index: 0}}}}
	if err := e.concatParts("j", filepath.Join(t.TempDir(), "x"), 2); err == nil {
		t.Fatal("expected an error when a part is missing")
	}
}

func TestLogEvent_CapsAndTruncates(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "t.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })

	job := &models.Job{ID: "j1", Status: models.JobCreated}
	if err := st.CreateJob(job); err != nil {
		t.Fatal(err)
	}

	e := &Engine{store: st, log: slog.New(slog.NewTextHandler(os.Stderr, nil))}

	for i := 0; i < maxJobEvents+5; i++ {
		e.LogEvent("j1", models.EventInfo, "event %d", i)
	}
	e.LogEvent("j1", models.EventWarn, "%s", strings.Repeat("x", 600))

	got, err := st.GetJob("j1")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Events) != maxJobEvents {
		t.Fatalf("want %d capped events, got %d", maxJobEvents, len(got.Events))
	}
	// oldest events should have been dropped, newest retained in order: 306
	// total events emitted (300+5 numbered + 1 oversized), capped to 300 means
	// the first 6 ("event 0".."event 5") fell off.
	if got.Events[0].Message != "event 6" {
		t.Errorf("expected oldest surviving event to be \"event 6\", got %q", got.Events[0].Message)
	}
	last := got.Events[len(got.Events)-1]
	if !strings.HasSuffix(last.Message, "…") || len(strings.TrimSuffix(last.Message, "…")) != 500 {
		t.Errorf("expected the oversized message truncated to 500 chars + ellipsis, got len=%d msg=%q", len(last.Message), last.Message)
	}
}
