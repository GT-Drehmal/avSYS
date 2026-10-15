package store

import (
	"context"
	"testing"
)

// A domain has one owner: claims of two organizations never overlap, and a
// hostname belongs to the organization whose domain it is under.
func TestDomains(t *testing.T) {
	s, ctx := open(t), context.Background()
	owner := user(t, s, "owner")
	a, b := &Org{Slug: "a", Name: "A"}, &Org{Slug: "b", Name: "B"}
	for _, o := range []*Org{a, b} {
		if err := s.CreateOrg(ctx, o, owner.ID); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.SetDomains(ctx, a.ID, []string{"a.example", "play.a.example"}); err != nil {
		t.Fatalf("A claims its own: %v", err)
	}
	if err := s.SetDomains(ctx, b.ID, []string{"b.example"}); err != nil {
		t.Fatalf("B claims its own: %v", err)
	}
	for _, d := range []string{"a.example", "x.a.example", "example"} {
		err := s.SetDomains(ctx, b.ID, []string{"b.example", d})
		if c, ok := IsClaimed(err); !ok || c.OrgID != a.ID {
			t.Errorf("B claiming %s: %v", d, err)
		}
	}
	// A refused claim changes nothing.
	if got, _ := s.Domains(ctx, b.ID); len(got) != 1 || got[0] != "b.example" {
		t.Errorf("B's after a refused claim: %v", got)
	}
	for host, want := range map[string]string{"a.example": a.ID, "mc.play.a.example": a.ID, "b.example": b.ID, "survival.b.example": b.ID, "c.example": "", "nota.example": ""} {
		if got, err := s.DomainOwner(ctx, host); err != nil || got != want {
			t.Errorf("owner of %s: %q %v, want %q", host, got, err, want)
		}
	}
	// Given up, it is free for another.
	if err := s.SetDomains(ctx, a.ID, nil); err != nil {
		t.Fatal(err)
	}
	if err := s.SetDomains(ctx, b.ID, []string{"b.example", "a.example"}); err != nil {
		t.Errorf("a domain given up: %v", err)
	}
	// And an organization's claims go with it.
	if err := s.DeleteOrg(ctx, b.ID); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.DomainOwner(ctx, "a.example"); got != "" {
		t.Errorf("a deleted organization still owns a.example: %q", got)
	}
}
