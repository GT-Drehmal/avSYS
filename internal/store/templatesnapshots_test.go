package store

import (
	"context"
	"testing"
	"time"
)

func TestTemplateSnapshots(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	owner := user(t, s, "owner")
	a, b := &Org{Slug: "a", Name: "A"}, &Org{Slug: "b", Name: "B"}
	for _, o := range []*Org{a, b} {
		if err := s.CreateOrg(ctx, o, owner.ID); err != nil {
			t.Fatal(err)
		}
	}
	// As restic says the time: to the nanosecond, in the zone it was taken in.
	zone := time.FixedZone("", 2*3600)
	at := time.Date(2026, 10, 1, 12, 0, 0, 123456789, zone)
	first := TemplateSnapshot{ID: "a1", Host: "template-t1", Time: at, By: "admin", Version: "1.21.4", Loader: "paper", Size: 10, Added: 4, Files: 3, Lineage: "l1"}
	second := TemplateSnapshot{ID: "b2", Host: "template-t1", Time: at.Add(time.Hour), Lineage: "b2"}
	// Taken in the same second, as copies can be.
	tied := TemplateSnapshot{ID: "a0", Host: "world-w1", Time: at.Add(time.Hour), Lineage: "a0"}

	if read, err := s.TemplateSnapshotsRead(ctx, a.ID); err != nil || read {
		t.Errorf("read before it was: %v %v", read, err)
	}
	for _, snap := range []TemplateSnapshot{first, second, tied} {
		if err := s.NoteTemplateSnapshot(ctx, a.ID, snap); err != nil {
			t.Fatal(err)
		}
	}
	// Noted twice is noted once.
	if err := s.NoteTemplateSnapshot(ctx, a.ID, first); err != nil {
		t.Fatal(err)
	}
	got, err := s.TemplateSnapshots(ctx, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[0].ID != "a0" || got[1].ID != "b2" || got[2].ID != "a1" {
		t.Fatalf("newest first, and by ID for the same time: %+v", got)
	}
	if got[2].Time.Format(time.RFC3339Nano) != "2026-10-01T12:00:00.123456789+02:00" {
		t.Errorf("its time: %s", got[2].Time.Format(time.RFC3339Nano))
	}
	got[2].Time = first.Time
	if got[2] != first {
		t.Errorf("kept as %+v, not %+v", got[2], first)
	}
	if other, err := s.TemplateSnapshots(ctx, b.ID); err != nil || len(other) != 0 {
		t.Errorf("another organization's: %v %v", other, err)
	}

	if err := s.ForgetTemplateSnapshots(ctx, a.ID, "b2", "not-there"); err != nil {
		t.Fatal(err)
	}
	if err := s.ForgetTemplateSnapshotsOf(ctx, a.ID, "world-w1"); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.TemplateSnapshots(ctx, a.ID); len(got) != 1 || got[0].ID != "a1" {
		t.Errorf("after forgetting: %+v", got)
	}

	// Set as the repository has them: what is new and what is gone is said.
	added, removed, err := s.SetTemplateSnapshots(ctx, a.ID, []TemplateSnapshot{second, tied})
	if err != nil || added != 2 || removed != 1 {
		t.Errorf("set: %d added, %d removed, %v", added, removed, err)
	}
	if read, err := s.TemplateSnapshotsRead(ctx, a.ID); err != nil || !read {
		t.Errorf("not read once set: %v %v", read, err)
	}
	if added, removed, err := s.SetTemplateSnapshots(ctx, a.ID, []TemplateSnapshot{second, tied}); err != nil || added != 0 || removed != 0 {
		t.Errorf("set again: %d added, %d removed, %v", added, removed, err)
	}
	if got, _ := s.TemplateSnapshots(ctx, a.ID); len(got) != 2 {
		t.Errorf("after setting: %+v", got)
	}
	if read, _ := s.TemplateSnapshotsRead(ctx, b.ID); read {
		t.Error("another organization's read")
	}

	// Gone with the organization.
	if err := s.DeleteOrg(ctx, a.ID); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.TemplateSnapshots(ctx, a.ID); len(got) != 0 {
		t.Errorf("after the organization: %+v", got)
	}
	if read, _ := s.TemplateSnapshotsRead(ctx, a.ID); read {
		t.Error("read after the organization")
	}
}
