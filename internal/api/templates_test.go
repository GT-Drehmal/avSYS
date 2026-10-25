package api

import (
	"testing"
	"time"

	"github.com/GT-Drehmal/avSYS/internal/backup"
	"github.com/GT-Drehmal/avSYS/internal/store"
)

func TestTemplateVersions(t *testing.T) {
	at := func(h int) time.Time { return time.Date(2026, 10, 1, h, 0, 0, 0, time.UTC) }
	// Newest first, as the repository lists them.
	list := []backup.Snapshot{
		{ID: "c3", Lineage: "l3", Server: "template-t1", Trigger: backup.TriggerTemplate, Time: at(3)},
		{ID: "x1", Lineage: "x1", Server: "template-t1", Trigger: backup.TriggerManual, Time: at(3)},
		{ID: "o1", Lineage: "o1", Server: "template-t2", Trigger: backup.TriggerTemplate, Time: at(2)},
		{ID: "b2", Lineage: "l2", Server: "template-t1", Trigger: backup.TriggerTemplate, Time: at(2)},
		{ID: "s1", Lineage: "s1", Server: "lobby", Trigger: backup.TriggerManual, Time: at(1)},
		{ID: "a1", Lineage: "l1", Server: "template-t1", Trigger: backup.TriggerTemplate, Time: at(1)},
	}
	tpl := &store.Template{ID: "t1", Name: "Lobby"}
	v := templateOf(tpl, list)
	var ids []string
	for _, s := range v.Versions {
		ids = append(ids, s.ID)
	}
	// Its own, and only those taken as its versions.
	if len(ids) != 3 || ids[0] != "c3" || ids[1] != "b2" || ids[2] != "a1" {
		t.Fatalf("versions: %v", ids)
	}
	if v.Current == nil || v.Current.ID != "c3" {
		t.Errorf("with none chosen the newest is used: %+v", v.Current)
	}
	// One chosen by its lineage, which outlasts its ID.
	tpl.Current = "l2"
	if v := templateOf(tpl, list); v.Current == nil || v.Current.ID != "b2" {
		t.Errorf("chosen: %+v", v.Current)
	}
	// One chosen that is gone: the newest.
	tpl.Current = "gone"
	if v := templateOf(tpl, list); v.Current == nil || v.Current.ID != "c3" {
		t.Errorf("chosen and gone: %+v", v.Current)
	}
	if v := templateOf(&store.Template{ID: "t9"}, list); v.Current != nil || len(v.Versions) != 0 {
		t.Errorf("one with no versions yet: %+v", v)
	}
}

func TestTemplateWords(t *testing.T) {
	for _, tc := range []struct {
		name, description string
		ok                bool
	}{
		{"  Lobby ", "Where everyone starts.\nWith a shop.", true},
		{"", "", false},
		{"   ", "", false},
		{"Two\nlines", "", false},
		{"Bell\a", "", false},
		{string(make([]rune, 61)), "", false},
		{"Long", string(make([]byte, 501)), false},
	} {
		name, _, err := templateWords(tc.name, tc.description)
		if (err == nil) != tc.ok {
			t.Errorf("%q: %v", tc.name, err)
		}
		if tc.ok && name != "Lobby" {
			t.Errorf("not tidied: %q", name)
		}
	}
}
