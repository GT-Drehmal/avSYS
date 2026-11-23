package api

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"sync"
	"time"

	avsysv1 "github.com/GT-Drehmal/avSYS/api/v1alpha1"
	"github.com/GT-Drehmal/avSYS/internal/controller"
)

// What Modrinth content needs is worked out when someone adds it, and put on
// the server's list beside it, on servers that have the image download none
// of it (controller.ListsDependencies): never at a start, nor in a look at
// the server's folders. For each required dependency of the version added,
// nothing is added where the server has it already: that project on the
// list, or a file on it or in its folders that gives the dependency's mod ID
// (a fork, a renamed jar, a mod's "provides", a jar inside another). Else it
// is added as an entry of its own, marked as a dependency of what needs it,
// at the version Modrinth names where that is for the server's game version
// and loader, or the newest that is; and what it needs is worked out in turn.
// Only mods, plugins and datapacks are added: never a modpack, resource pack
// or shader. A version already on the list is never changed. A dependency
// goes when the last entry that needs it does, or when content added
// gives what it is.

// errNeedsUnknown is what a change is refused with when Modrinth does not
// answer: what it needs is not known, and nothing is changed.
func errNeedsUnknown(name string) error {
	return &Error{Status: http.StatusBadGateway, Code: "upstream", Message: fmt.Sprintf("Modrinth is not answering, so what %s needs cannot be worked out. Nothing was changed. Try again in a moment.", name)}
}

// modrinthBackoff is how long the panel waits before asking Modrinth again.
var modrinthBackoff = 500 * time.Millisecond

// fromModrinth asks up to three times, while there is no answer or an
// error: whether a change can be made depends on it. Not found is an answer.
func fromModrinth[T any](ctx context.Context, ask func() (T, error)) (T, error) {
	var v T
	var err error
	for try := range 3 {
		if try > 0 {
			select {
			case <-ctx.Done():
				return v, err
			case <-time.After(time.Duration(try) * modrinthBackoff):
			}
		}
		if v, err = ask(); err == nil || errors.Is(err, errNotFound) {
			return v, err
		}
	}
	return v, err
}

type modrinthProjectInfo struct {
	ID          string `json:"id"`
	Slug        string `json:"slug"`
	Title       string `json:"title"`
	IconURL     string `json:"icon_url"`
	ProjectType string `json:"project_type"`
}

func (s *Server) modrinthProjectInfo(ctx context.Context, id string) (*modrinthProjectInfo, error) {
	body, err := s.catalog.get(ctx, "https://api.modrinth.com/v2/project/"+url.PathEscape(id), time.Hour)
	if err != nil {
		return nil, err
	}
	var p modrinthProjectInfo
	return &p, json.Unmarshal(body, &p)
}

// keepPanelNotes gives each entry what the panel noted of it before, rather
// than what a request says: the mod IDs its file gives (for the same
// version), and whether the panel added it for what needs it.
func keepPanelNotes(spec, previous *avsysv1.MinecraftServerSpec) {
	gives, added := map[string][]string{}, map[string]*avsysv1.ContentRef{}
	if previous != nil {
		for i := range previous.Mods {
			p := &previous.Mods[i]
			gives[givesKey(p)] = p.Provides
			added[string(p.Source)+"|"+p.Project] = p
		}
	}
	for i := range spec.Mods {
		m := &spec.Mods[i]
		m.Provides = slices.Clone(gives[givesKey(m)])
		m.Dependency, m.NeededBy = false, nil
		if p := added[string(m.Source)+"|"+m.Project]; p != nil {
			m.Dependency, m.NeededBy = p.Dependency, slices.Clone(p.NeededBy)
		}
	}
}

func givesKey(m *avsysv1.ContentRef) string {
	return string(m.Source) + "|" + string(cmp.Or(m.Kind, avsysv1.KindMod)) + "|" + m.Project + "|" + m.Version
}

// listDependencies lists what the Modrinth entries added to spec need, and
// takes off the dependencies nothing on it needs now. previous is the spec
// being replaced, or nil for a new server.
func (s *Server) listDependencies(ctx context.Context, spec, previous *avsysv1.MinecraftServerSpec, found []FoundFile) error {
	if !controller.ListsDependencies(spec) {
		return nil
	}
	if err := stillNeeded(spec, previous); err != nil {
		return err
	}
	isNew := func(m *avsysv1.ContentRef) bool {
		if previous == nil {
			return true
		}
		return !slices.ContainsFunc(previous.Mods, func(p avsysv1.ContentRef) bool { return givesKey(&p) == givesKey(m) })
	}
	replaceProvided(spec, isNew)
	// A server the image downloaded dependencies for until this change (its
	// own env said so, or it ran a modpack) has what all its entries need
	// worked out now: from here the image downloads none.
	all := previous != nil && !controller.ListsDependencies(previous)
	var added []int
	for i := range spec.Mods {
		if m := &spec.Mods[i]; m.Source == avsysv1.SourceModrinth && m.Version != "" && (all || isNew(m)) {
			added = append(added, i)
		}
	}
	if err := s.addNeeded(ctx, spec, added, found); err != nil {
		return err
	}
	pruneDependencies(spec)
	return nil
}

// stillNeeded refuses taking off an entry other content on the list needs,
// whoever added it, while that stays.
func stillNeeded(spec, previous *avsysv1.MinecraftServerSpec) error {
	if previous == nil {
		return nil
	}
	on := map[string]string{}
	for _, m := range spec.Mods {
		on[m.Project] = cmpOr(m.Title, m.Project)
	}
	for _, p := range previous.Mods {
		if _, kept := on[p.Project]; kept {
			continue
		}
		for _, by := range p.NeededBy {
			if title, ok := on[by]; ok {
				return errBadRequest("%s is needed by %s. Remove that first.", cmpOr(p.Title, p.Project), title)
			}
		}
	}
	return nil
}

// pruneDependencies takes off the dependencies nothing that someone added
// needs, by way of others or itself, and forgets what needed them that is
// gone.
func pruneDependencies(spec *avsysv1.MinecraftServerSpec) {
	keep := map[string]bool{}
	for _, m := range spec.Mods {
		if !m.Dependency {
			keep[m.Project] = true
		}
	}
	for grew := true; grew; {
		grew = false
		for _, m := range spec.Mods {
			if m.Dependency && !keep[m.Project] && slices.ContainsFunc(m.NeededBy, func(by string) bool { return keep[by] }) {
				keep[m.Project], grew = true, true
			}
		}
	}
	spec.Mods = slices.DeleteFunc(spec.Mods, func(m avsysv1.ContentRef) bool { return m.Dependency && !keep[m.Project] })
	for i := range spec.Mods {
		spec.Mods[i].NeededBy = slices.DeleteFunc(spec.Mods[i].NeededBy, func(by string) bool { return !keep[by] })
	}
}

// replaceProvided takes off each dependency the panel added whose mod ID
// content someone just added gives (isNew): a fork, say, uploaded where the
// panel had added the original. What needed that dependency, and what it
// needed, are then needed by and needed by what took its place.
func replaceProvided(spec *avsysv1.MinecraftServerSpec, isNew func(*avsysv1.ContentRef) bool) {
	gives := map[string]string{}
	for i := range spec.Mods {
		if m := &spec.Mods[i]; !m.Dependency && isNew(m) {
			for _, id := range m.Provides {
				gives[id] = m.Project
			}
		}
	}
	instead, needs := map[string]string{}, map[string][]string{}
	spec.Mods = slices.DeleteFunc(spec.Mods, func(m avsysv1.ContentRef) bool {
		if !m.Dependency || len(m.Provides) == 0 || gives[m.Provides[0]] == "" {
			return false
		}
		p := gives[m.Provides[0]]
		instead[m.Project], needs[p] = p, append(needs[p], m.NeededBy...)
		return true
	})
	for i := range spec.Mods {
		m := &spec.Mods[i]
		by := append(m.NeededBy, needs[m.Project]...)
		for j := range by {
			if p, ok := instead[by[j]]; ok {
				by[j] = p
			}
		}
		slices.Sort(by)
		m.NeededBy = slices.DeleteFunc(slices.Compact(by), func(b string) bool { return b == m.Project })
		if len(m.NeededBy) == 0 {
			m.NeededBy = nil
		}
	}
}

// maxDependencies bounds how many the panel adds in one change.
const maxDependencies = 200

// skipped are dependencies the panel does not add, said in its log once.
var skipped sync.Map

// addNeeded adds what the entries at added need, and what that needs.
func (s *Server) addNeeded(ctx context.Context, spec *avsysv1.MinecraftServerSpec, added []int, found []FoundFile) error {
	t := targetOf(spec)
	// The mod IDs the server has, and the entry that gives each (-1 for a
	// file in its folders).
	has := map[string]int{}
	for i, m := range spec.Mods {
		for _, id := range m.Provides {
			if _, ok := has[id]; !ok {
				has[id] = i
			}
		}
	}
	for _, f := range found {
		for _, id := range f.Provides {
			if _, ok := has[id]; !ok {
				has[id] = -1
			}
		}
	}
	// What needs it, noted on the entry that gives it, whoever added that.
	need := func(at int, by string) {
		if d := &spec.Mods[at]; d.Project != by && !slices.Contains(d.NeededBy, by) {
			d.NeededBy = append(d.NeededBy, by)
		}
	}
	skip := func(project, why string) {
		if _, said := skipped.LoadOrStore(project+" "+why, true); !said {
			s.Log.Info("a dependency is not added", "project", project, "why", why)
		}
	}
	for n := 0; len(added) > 0; {
		by := spec.Mods[added[0]]
		added = added[1:]
		name := cmpOr(by.Title, by.Project)
		v, err := fromModrinth(ctx, func() (*modrinthVersion, error) { return s.modrinthVersion(ctx, by.Version) })
		if errors.Is(err, errNotFound) {
			continue
		} else if err != nil {
			return errNeedsUnknown(name)
		}
		for _, d := range v.Dependencies {
			if d.Type != "required" {
				continue
			}
			var named *modrinthVersion
			if d.VersionID != "" {
				named, err = fromModrinth(ctx, func() (*modrinthVersion, error) { return s.modrinthVersion(ctx, d.VersionID) })
				if err != nil && !errors.Is(err, errNotFound) {
					return errNeedsUnknown(name)
				}
				if named != nil && d.ProjectID == "" {
					d.ProjectID = named.ProjectID
				}
			}
			if d.ProjectID == "" {
				continue
			}
			p, err := fromModrinth(ctx, func() (*modrinthProjectInfo, error) { return s.modrinthProjectInfo(ctx, d.ProjectID) })
			if errors.Is(err, errNotFound) {
				skip(d.ProjectID, "not on Modrinth")
				continue
			} else if err != nil {
				return errNeedsUnknown(name)
			}
			// That project on the list, or found in the folders.
			if at := slices.IndexFunc(spec.Mods, func(m avsysv1.ContentRef) bool {
				return m.Source == avsysv1.SourceModrinth && (m.Project == p.ID || m.Project == p.Slug)
			}); at >= 0 {
				need(at, by.Project)
				continue
			}
			if slices.ContainsFunc(found, func(f FoundFile) bool { return f.Project == p.ID }) {
				continue
			}
			switch p.ProjectType {
			case "mod", "plugin", "datapack":
			default:
				skip(p.ID, "a "+p.ProjectType)
				continue
			}
			kind, chosen, err := s.dependencyVersion(ctx, p.ID, named, t)
			if err != nil {
				return errNeedsUnknown(name)
			}
			if chosen == nil {
				skip(p.ID, "no version for "+t.String())
				continue
			}
			// What its file says it is, which something on the server may be.
			if kind != avsysv1.KindDatapack && chosen.file() != "" {
				info, err := fromModrinth(ctx, func() (jarInfo, error) { return inspectURL(ctx, s.downloads(), chosen.file()) })
				if err != nil {
					return errNeedsUnknown(name)
				}
				if len(info.ids) > 0 {
					if at, ok := has[info.ids[0]]; ok {
						if at >= 0 {
							need(at, by.Project)
						}
						continue
					}
				}
			}
			if n++; n > maxDependencies {
				return errBadRequest("%s needs more than %d other projects.", name, maxDependencies)
			}
			entry := avsysv1.ContentRef{Source: avsysv1.SourceModrinth, Kind: kind, Project: p.ID, Version: chosen.ID, Title: p.Title, IconURL: p.IconURL}
			if _, err := fromModrinth(ctx, func() (bool, error) { return true, s.pin(ctx, &entry, t) }); err != nil {
				return errNeedsUnknown(name)
			}
			entry.Dependency, entry.NeededBy = true, []string{by.Project}
			spec.Mods = append(spec.Mods, entry)
			at := len(spec.Mods) - 1
			for _, id := range entry.Provides {
				if _, ok := has[id]; !ok {
					has[id] = at
				}
			}
			added = append(added, at)
		}
	}
	return nil
}

// dependencyVersion is the version of a dependency to add, and as what: the
// one Modrinth named, where it is for the server's game version and loader,
// or else the newest that is. Code (a mod or plugin) is preferred to a
// datapack. Nil where none is.
func (s *Server) dependencyVersion(ctx context.Context, project string, named *modrinthVersion, t target) (avsysv1.ContentKind, *modrinthVersion, error) {
	kinds := []avsysv1.ContentKind{avsysv1.KindDatapack}
	switch t.loader {
	case "paper", "purpur", "folia":
		kinds = []avsysv1.ContentKind{avsysv1.KindPlugin, avsysv1.KindDatapack}
	case "fabric", "quilt", "forge", "neoforge":
		kinds = []avsysv1.ContentKind{avsysv1.KindMod, avsysv1.KindDatapack}
	}
	fits := func(v *modrinthVersion, kind avsysv1.ContentKind) bool {
		return (t.gameVersion == "" || slices.Contains(v.GameVersions, t.gameVersion)) &&
			slices.ContainsFunc(v.Loaders, func(l string) bool { return slices.Contains(t.loadersFor(kind), l) })
	}
	if named != nil {
		for _, kind := range kinds {
			if fits(named, kind) {
				return kind, named, nil
			}
		}
	}
	for _, kind := range kinds {
		versions, err := fromModrinth(ctx, func() ([]modrinthVersion, error) {
			return s.modrinthVersionList(ctx, project, t.loadersFor(kind), t.gameVersion)
		})
		if errors.Is(err, errNotFound) {
			return "", nil, nil
		} else if err != nil {
			return "", nil, err
		}
		if versions = preferReleases(versions); len(versions) > 0 {
			return kind, &versions[0], nil
		}
	}
	return "", nil, nil
}
