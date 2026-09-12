package store

import (
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/rokelvisar/storyframe/backend/internal/models"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "test.sqlite"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestCreateGetUpdate(t *testing.T) {
	s := newTestStore(t)
	job := &models.Job{
		ID:       "j1",
		Filename: "a.mp4",
		Status:   models.JobCreated,
		Segments: []*models.TimelineSegment{{ID: "s1", Index: 0}, {ID: "s2", Index: 1}},
	}
	if err := s.CreateJob(job); err != nil {
		t.Fatalf("create: %v", err)
	}

	got, err := s.GetJob("j1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Filename != "a.mp4" || len(got.Segments) != 2 {
		t.Fatalf("unexpected job: %+v", got)
	}
	if got.CreatedAt.IsZero() {
		t.Error("CreatedAt not set")
	}

	if _, err := s.GetJob("missing"); err != ErrNotFound {
		t.Fatalf("want ErrNotFound, got %v", err)
	}

	updated, err := s.UpdateJob("j1", func(j *models.Job) error {
		j.Status = models.JobComplete
		FindSegment(j, "s2").Status = models.SegDone
		return nil
	})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if updated.Status != models.JobComplete || FindSegment(updated, "s2").Status != models.SegDone {
		t.Fatalf("update not applied: %+v", updated)
	}
	reloaded, _ := s.GetJob("j1")
	if reloaded.Status != models.JobComplete {
		t.Fatalf("not persisted: %+v", reloaded)
	}
}

func TestUpdateJob_ConcurrentSegmentWrites(t *testing.T) {
	s := newTestStore(t)
	segs := make([]*models.TimelineSegment, 20)
	for i := range segs {
		segs[i] = &models.TimelineSegment{ID: string(rune('a' + i)), Index: i}
	}
	if err := s.CreateJob(&models.Job{ID: "j", Status: models.JobCreated, Segments: segs}); err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	for i := range segs {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			_, err := s.UpdateJob("j", func(j *models.Job) error {
				FindSegment(j, id).Status = models.SegDone
				return nil
			})
			if err != nil {
				t.Errorf("update %s: %v", id, err)
			}
		}(segs[i].ID)
	}
	wg.Wait()

	got, _ := s.GetJob("j")
	for _, seg := range got.Segments {
		if seg.Status != models.SegDone {
			t.Fatalf("segment %s lost its update (status %q)", seg.ID, seg.Status)
		}
	}
}

func TestListJobs_NewestFirstAndLimit(t *testing.T) {
	s := newTestStore(t)
	for _, id := range []string{"j1", "j2", "j3"} {
		if err := s.CreateJob(&models.Job{ID: id, Filename: id + ".mp4", Status: models.JobCreated}); err != nil {
			t.Fatal(err)
		}
		time.Sleep(2 * time.Millisecond) // created_at has millisecond-ish resolution; keep ordering deterministic
	}

	got, err := s.ListJobs(0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("want 3 summaries, got %d", len(got))
	}
	if got[0].ID != "j3" || got[1].ID != "j2" || got[2].ID != "j1" {
		t.Fatalf("want newest-first order [j3 j2 j1], got %v", []string{got[0].ID, got[1].ID, got[2].ID})
	}
	if got[0].Filename != "j3.mp4" {
		t.Fatalf("summary missing filename: %+v", got[0])
	}

	limited, err := s.ListJobs(2)
	if err != nil {
		t.Fatal(err)
	}
	if len(limited) != 2 {
		t.Fatalf("want 2 summaries with limit=2, got %d", len(limited))
	}
}
