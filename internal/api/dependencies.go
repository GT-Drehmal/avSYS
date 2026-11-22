package api

import (
	"archive/zip"
	"context"
	"errors"
	"fmt"
	"net/http"
	"path"
	"slices"
	"sort"
	"strings"
	"sync"

	avsysv1 "github.com/GT-Drehmal/avSYS/api/v1alpha1"
	"github.com/GT-Drehmal/avSYS/internal/controller"
)

// What Modrinth content needs is listed with it, by the panel, rather than
// downloaded by the server's image as it starts. The image goes by
// Modrinth's projects alone: a mod a server has as an upload, a fork of it,
// a file renamed, or one carried inside another mod, it does not see, and
// it would download a second copy beside it, which the loader refuses to
// start with. The panel goes by what the files say they are:
//
//   - For each Modrinth entry, what its pinned version needs, as Modrinth
//     says. Optional, incompatible and embedded projects are not needed.
//   - Each project needed that the list has is that one. One it does not
//     have is, as the loader knows it, the mod ID its file says it is
//     (the version Modrinth names for it, or the one the panel would pin).
//   - If any file on the server gives that ID (an upload, a Modrinth or
//     CurseForge mod, one from an address, one found in its folders, or a
//     jar inside any of these), that is what it needs. Where the server's
//     is not a version the content says it works with, the Content page
//     says so: a second copy would not start either.
//   - Otherwise the project is added, pinned, as an entry of its own marked
//     as a dependency, and what it needs in turn too. One that nothing
//     needs any more is taken off.
//
// The image is then told not to download what content needs
// (controller.DependenciesListed).

// depOptions are how a list's dependencies are worked out.
type depOptions struct {
	// add says what is needed is added, and what nothing needs any more
	// taken off; without it the list is only noted as it is.
	add bool
	// installed are the versions of projects the server has files of, by
	// project ID, which are the ones it gets.
	installed map[string][]*modrinthVersion
	// onServer is what is in the server's folders, if known.
	onServer *serverContent
}

// serverContent is what a server has in its folders besides its list, for
// working out what it provides: the files found there, and its files to
// read them, where they can be reached.
type serverContent struct {
	found []FoundFile
	files func() ServerFiles
	once  sync.Once
	got   ServerFiles
}

func (c *serverContent) reach() ServerFiles {
	if c == nil || c.files == nil {
		return nil
	}
	c.once.Do(func() { c.got = c.files() })
	return c.got
}

// provider is something on a server that gives mod IDs: an entry of its
// list (index), or a file found in its folders (found).
type provider struct {
	index int
	found *FoundFile
	ids   []string
	// meta is what its file says, where it was read.
	meta *modMeta
}

// depLister works out one list's dependencies.
type depLister struct {
	s    *Server
	ctx  context.Context
	spec *avsysv1.MinecraftServerSpec
	t    target
	opts depOptions

	// infos are the versions the list's Modrinth entries are pinned to, by
	// index; nil for one that is not pinned.
	infos []*modrinthVersion
	// providers, by kind family: "mod" or "plugin".
	providers map[string][]*provider
	gathered  bool
	// reached are the entries something on the list needs, or that are
	// the list's own.
	reached map[int]bool
}

func family(kind avsysv1.ContentKind) string {
	if kind == avsysv1.KindPlugin {
		return "plugin"
	}
	return "mod"
}

func kindOf(m avsysv1.ContentRef) avsysv1.ContentKind {
	if m.Kind == "" {
		return avsysv1.KindMod
	}
	return m.Kind
}

// listDependencies works out what a server's Modrinth content needs, and
// with opts.add makes its list have it: what is needed and not on the
// server is added, and what the panel added that nothing needs any more is
// taken off. Each entry is noted with what needs it, and with what it needs
// another version of than the server has.
func (s *Server) listDependencies(ctx context.Context, spec *avsysv1.MinecraftServerSpec, opts depOptions) error {
	if !controller.ListsDependencies(spec) {
		return nil
	}
	// Worked out on a copy: the list is changed only once it all is.
	work := spec.DeepCopy()
	l := &depLister{s: s, ctx: ctx, spec: work, t: targetOf(spec), opts: opts}
	if err := l.pinnedVersions(); err != nil {
		return err
	}
	if err := l.resolve(); err != nil {
		return err
	}
	*spec = *work
	return nil
}

// pinnedVersions looks up the version each Modrinth entry is pinned to,
// a few at a time.
func (l *depLister) pinnedVersions() error {
	l.infos = make([]*modrinthVersion, len(l.spec.Mods))
	var (
		wg    sync.WaitGroup
		slots = make(chan struct{}, 4)
		mu    sync.Mutex
		first error
	)
	for i, m := range l.spec.Mods {
		if m.Source != avsysv1.SourceModrinth || m.Version == "" {
			continue
		}
		wg.Add(1)
		slots <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-slots }()
			v, err := l.s.modrinthVersion(l.ctx, m.Version)
			mu.Lock()
			defer mu.Unlock()
			// One Modrinth no longer has needs what it needs: not known.
			if errors.Is(err, errNotFound) {
				return
			}
			if err != nil {
				if first == nil {
					first = fmt.Errorf("looking up what %s needs: %w", cmpOr(m.Title, m.Project), err)
				}
				return
			}
			l.infos[i] = v
		}()
	}
	wg.Wait()
	return first
}

// projectOf is the Modrinth project ID of an entry, as its version says;
// as it is named where that is not known.
func (l *depLister) projectOf(i int) string {
	if i < len(l.infos) && l.infos[i] != nil && l.infos[i].ProjectID != "" {
		return l.infos[i].ProjectID
	}
	return l.spec.Mods[i].Project
}

func (l *depLister) resolve() error {
	mods := &l.spec.Mods
	// What the list has of each project. A dependency the panel added that
	// the server has another file of now, as an upload put there since,
	// is not: that file is what is needed, and the one added goes.
	// Nor is one that someone added themselves since, by another name of
	// it: theirs is the one.
	listed := map[string]int{}
	for _, added := range []bool{false, true} {
		for i, m := range *mods {
			if m.Source != avsysv1.SourceModrinth || m.Dependency != added {
				continue
			}
			if m.Dependency && l.opts.add {
				if p := l.otherProvider(i); p != nil {
					continue
				}
			}
			if _, twice := listed[l.projectOf(i)]; !twice {
				listed[l.projectOf(i)] = i
			}
		}
	}
	neededBy := map[int][]string{}
	unmet := map[int][]avsysv1.UnmetDependency{}
	l.reached = map[int]bool{}
	reached := l.reached
	// What needs what, a version's dependencies at a time: those of an
	// entry's pinned version, and those of the version of a project that
	// something else on the server is, which that is taken to need too.
	type needs struct {
		// i is the entry that needs them, whose file says which versions of
		// them it works with; -1 for none.
		i int
		// by is what needs them, as what needs an entry is noted.
		by   string
		kind avsysv1.ContentKind
		// self is the project they are of, which does not need itself.
		self string
		deps []modrinthDependency
	}
	var queue []needs
	visit := func(i int) {
		reached[i] = true
		if info := l.infos[i]; info != nil {
			queue = append(queue, needs{i: i, by: (*mods)[i].Project, kind: kindOf((*mods)[i]), self: l.projectOf(i), deps: info.Dependencies})
		}
	}
	for i, m := range *mods {
		if m.Source == avsysv1.SourceModrinth && !m.Dependency {
			visit(i)
		}
	}
	// Without adding, all listed is as it is: what it needs is noted.
	if !l.opts.add {
		for i, m := range *mods {
			if m.Source == avsysv1.SourceModrinth && m.Dependency && !reached[i] {
				visit(i)
			}
		}
	}
	chained := map[string]bool{}
	for len(queue) > 0 {
		n := queue[0]
		queue = queue[1:]
		for _, d := range n.deps {
			if d.Type != "required" {
				continue
			}
			project := d.ProjectID
			var named *modrinthVersion
			if d.VersionID != "" {
				v, err := l.s.modrinthVersion(l.ctx, d.VersionID)
				switch {
				case errors.Is(err, errNotFound):
					// A version Modrinth no longer has: its project's, as
					// for one named by its project, if that is said.
				case err != nil:
					return fmt.Errorf("looking up what %s needs: %w", l.nameOfNeeds(n.i, n.by), err)
				default:
					named = v
					if project == "" {
						project = v.ProjectID
					}
				}
			}
			if project == "" || project == n.self {
				continue
			}
			if j, ok := listed[project]; ok {
				neededBy[j] = appendOnce(neededBy[j], n.by)
				if !reached[j] {
					visit(j)
				}
				continue
			}
			depKind := n.kind
			chosen, err := l.versionOf(project, named, &depKind)
			if err != nil {
				return err
			}
			if chosen == nil {
				if l.opts.add {
					return errBadRequest("%s needs %s, which has no version for %s.", l.nameOfNeeds(n.i, n.by), l.nameOf(project), l.t)
				}
				continue
			}
			// As the loader knows it: by the mod ID its file says it is.
			info, ierr := l.inspect(chosen)
			id := ""
			if ierr == nil && depKind != avsysv1.KindDatapack {
				id = info.of(depKind).primary()
			}
			if id != "" {
				if p := l.providerOf(id, depKind, -1); p != nil {
					var by string
					if p.found == nil {
						neededBy[p.index] = appendOnce(neededBy[p.index], n.by)
						by = (*mods)[p.index].Project
					} else {
						by = foundName(*p.found)
					}
					if n.i >= 0 {
						if u, ok := l.unmet(n.i, id, p); ok {
							unmet[n.i] = append(unmet[n.i], u)
						}
					}
					// What is there in its place, as a fork of it, is taken
					// to need what it needs: a Modrinth entry says so itself.
					key := by + "\x00" + project
					if (p.found != nil || (*mods)[p.index].Source != avsysv1.SourceModrinth) && !chained[key] {
						chained[key] = true
						queue = append(queue, needs{i: -1, by: by, kind: depKind, self: project, deps: chosen.Dependencies})
					}
					continue
				}
			}
			if !l.opts.add {
				continue
			}
			m := avsysv1.ContentRef{Source: avsysv1.SourceModrinth, Kind: depKind, Project: project, Version: chosen.ID, Dependency: true}
			if err := l.s.pin(l.ctx, &m, l.t); err != nil {
				return err
			}
			if len(m.Provides) == 0 && ierr == nil {
				m.Provides = info.of(depKind).all()
			}
			*mods = append(*mods, m)
			j := len(*mods) - 1
			l.infos = append(l.infos, chosen)
			listed[project] = j
			neededBy[j] = []string{n.by}
			visit(j)
			if l.gathered && depKind != avsysv1.KindDatapack {
				l.providers[family(depKind)] = append(l.providers[family(depKind)], &provider{index: j, ids: m.Provides})
			}
		}
	}

	out := (*mods)[:0:0]
	for i, m := range *mods {
		// What the panel added that nothing needs any more goes with what
		// needed it.
		if l.opts.add && m.Source == avsysv1.SourceModrinth && m.Dependency && !reached[i] {
			continue
		}
		m.NeededBy = nil
		for _, p := range neededBy[i] {
			if p != m.Project {
				m.NeededBy = appendOnce(m.NeededBy, p)
			}
		}
		sort.Strings(m.NeededBy)
		m.Unmet = unmet[i]
		out = append(out, m)
	}
	*mods = out
	if l.opts.add {
		l.spec.DependenciesListed = true
	}
	return nil
}

func appendOnce(list []string, s string) []string {
	if slices.Contains(list, s) {
		return list
	}
	return append(list, s)
}

// nameOfNeeds is what needs something, for saying so.
func (l *depLister) nameOfNeeds(i int, by string) string {
	if i >= 0 {
		return entryName(l.spec.Mods[i])
	}
	for _, m := range l.spec.Mods {
		if m.Project == by {
			return entryName(m)
		}
	}
	return by
}

// nameOf is a project's name, for saying so.
func (l *depLister) nameOf(project string) string {
	m := avsysv1.ContentRef{Source: avsysv1.SourceModrinth, Project: project}
	l.s.describe(l.ctx, &m)
	return cmpOr(m.Title, project)
}

// versionOf is the version of a project that a server would have: the one
// it has a file of (the one Modrinth names, of two), the one Modrinth names,
// or the one the panel pins, the newest that fits. For a datapack, which
// can need a mod, the kind it is found as is said in kind. nil where none
// fits.
func (l *depLister) versionOf(project string, named *modrinthVersion, kind *avsysv1.ContentKind) (*modrinthVersion, error) {
	if has := l.opts.installed[project]; len(has) > 0 {
		for _, v := range has {
			if named != nil && v.ID == named.ID {
				return v, nil
			}
		}
		return has[0], nil
	}
	if named != nil {
		return named, nil
	}
	kinds := []avsysv1.ContentKind{*kind}
	if *kind == avsysv1.KindDatapack {
		if takes := l.t.loader; takes == "paper" || takes == "purpur" || takes == "folia" {
			kinds = append(kinds, avsysv1.KindPlugin)
		} else if takes != "vanilla" {
			kinds = append(kinds, avsysv1.KindMod)
		}
	}
	for _, k := range kinds {
		versions, err := l.s.modrinthVersionList(l.ctx, project, l.t.loadersFor(k), l.t.gameVersion)
		if errors.Is(err, errNotFound) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		if versions = preferReleases(versions); len(versions) > 0 {
			*kind = k
			return &versions[0], nil
		}
	}
	return nil, nil
}

// inspect reads a version's file, once.
func (l *depLister) inspect(v *modrinthVersion) (jarInfo, error) {
	if v.file() == "" {
		return jarInfo{}, errors.New("no file")
	}
	return l.s.inspectURL(l.ctx, v.file())
}

// gather finds what gives the server mod IDs: the entries the list has of
// its own, and the files found in its folders. What the panel added as a
// dependency is one only once something reaches it.
func (l *depLister) gather() {
	if l.gathered {
		return
	}
	l.gathered = true
	l.providers = map[string][]*provider{}
	for i := range l.spec.Mods {
		m := &l.spec.Mods[i]
		if kindOf(*m) == avsysv1.KindDatapack {
			continue
		}
		p := &provider{index: i}
		l.fillProvides(i, p)
		l.providers[family(kindOf(*m))] = append(l.providers[family(kindOf(*m))], p)
	}
	if l.opts.onServer == nil {
		return
	}
	for i := range l.opts.onServer.found {
		f := &l.opts.onServer.found[i]
		if f.Kind == avsysv1.KindDatapack || f.Folder {
			continue
		}
		meta, ok := l.metaOfFound(f)
		if !ok {
			continue
		}
		l.providers[family(f.Kind)] = append(l.providers[family(f.Kind)], &provider{index: -1, found: f, ids: meta.all(), meta: &meta})
	}
}

// fillProvides learns what an entry's file gives the server, where the
// entry does not say yet, and notes it on the entry.
func (l *depLister) fillProvides(i int, p *provider) {
	m := &l.spec.Mods[i]
	kind := kindOf(*m)
	fileURL := ""
	switch m.Source {
	case avsysv1.SourceModrinth:
		if i < len(l.infos) && l.infos[i] != nil {
			fileURL = l.infos[i].file()
		}
	case avsysv1.SourceURL:
		fileURL = m.Project
	}
	// Noted already: the file is read again only to compare versions.
	if len(m.Provides) > 0 {
		p.ids = m.Provides
		return
	}
	if fileURL != "" {
		if info, err := l.s.inspectURL(l.ctx, fileURL); err == nil {
			meta := info.of(kind)
			p.meta = &meta
			m.Provides = meta.all()
		}
	}
	if m.Source == avsysv1.SourceUpload && p.meta == nil {
		if files := l.opts.onServer.reach(); files != nil {
			if meta, ok := metaOfServerFile(files, uploadPath(*m), kind, ""); ok {
				p.meta = &meta
				if len(m.Provides) == 0 {
					m.Provides = meta.all()
				}
			}
		}
	}
	p.ids = m.Provides
}

// metaOfFound reads what a file found in a server's folders says it is.
func (l *depLister) metaOfFound(f *FoundFile) (modMeta, bool) {
	if f.SHA1 != "" {
		if info, ok := inspected.get("sha1:" + f.SHA1); ok {
			return info.of(f.Kind), true
		}
	}
	files := l.opts.onServer.reach()
	if files == nil {
		return modMeta{}, false
	}
	return metaOfServerFile(files, path.Join(dataRoot, f.Path), f.Kind, f.SHA1)
}

// metaOfServerFile reads what a jar in a server's files says it is: only
// the parts of it that say, where its files can be read at any place.
// With its SHA-1 known, the answer is kept.
func metaOfServerFile(files ServerFiles, full string, kind avsysv1.ContentKind, sha1 string) (modMeta, bool) {
	info, err := files.Stat(full)
	if err != nil || info.IsDir() || info.Size() > maxJar {
		return modMeta{}, false
	}
	f, err := files.Open(full)
	if err != nil {
		return modMeta{}, false
	}
	defer f.Close()
	zr, err := zip.NewReader(f, info.Size())
	if err != nil {
		return modMeta{}, false
	}
	j := jarInfo{mod: readModMeta(zr, avsysv1.KindMod), plugin: readModMeta(zr, avsysv1.KindPlugin)}
	if sha1 != "" {
		inspected.keep("sha1:"+sha1, j)
	}
	return j.of(kind), true
}

// providerOf is what on the server gives a mod ID, but the entry except:
// first an entry of the list's own, then one the panel added that is
// needed, then a file found in its folders.
func (l *depLister) providerOf(id string, kind avsysv1.ContentKind, except int) *provider {
	if kind == avsysv1.KindDatapack {
		return nil
	}
	l.gather()
	var fallback *provider
	for _, p := range l.providers[family(kind)] {
		if (p.found == nil && p.index == except) || !slices.Contains(p.ids, id) {
			continue
		}
		if p.found == nil && l.spec.Mods[p.index].Dependency {
			if fallback == nil && l.reached[p.index] {
				fallback = p
			}
			continue
		}
		return p
	}
	return fallback
}

// otherProvider is an entry of the list's own, or a file found in the
// server's folders, that gives what a dependency the panel added is: the
// mod ID its file says it is.
func (l *depLister) otherProvider(i int) *provider {
	m := l.spec.Mods[i]
	id := ""
	if len(m.Provides) > 0 {
		id = m.Provides[0]
	} else if l.infos[i] != nil {
		if info, err := l.inspect(l.infos[i]); err == nil {
			id = info.of(kindOf(m)).primary()
		}
	}
	if id == "" || kindOf(m) == avsysv1.KindDatapack {
		return nil
	}
	l.gather()
	for _, p := range l.providers[family(kindOf(m))] {
		if p.found == nil && (p.index == i || l.spec.Mods[p.index].Dependency) {
			continue
		}
		if slices.Contains(p.ids, id) {
			return p
		}
	}
	return nil
}

// unmet says whether what the server has of a mod ID is not a version the
// entry i works with, as its file says.
func (l *depLister) unmet(i int, id string, p *provider) (avsysv1.UnmetDependency, bool) {
	info := l.infos[i]
	if info == nil {
		return avsysv1.UnmetDependency{}, false
	}
	needs, err := l.inspect(info)
	if err != nil {
		return avsysv1.UnmetDependency{}, false
	}
	need := needs.of(kindOf(l.spec.Mods[i]))
	ranges, ok := need.Depends[id]
	if !ok || len(ranges) == 0 {
		return avsysv1.UnmetDependency{}, false
	}
	has, known := "", false
	by := ""
	if meta := l.metaOf(p); meta != nil {
		has, known = meta.versionOf(id)
	}
	if p.found != nil {
		by = foundName(*p.found)
	} else {
		m := l.spec.Mods[p.index]
		by = entryName(m)
		// An upload is its own version, as it says; what is inside it is
		// not known without its file.
		if !known && m.Source == avsysv1.SourceUpload && len(m.Provides) > 0 && m.Provides[0] == id && m.VersionName != "" {
			has, known = m.VersionName, true
		}
	}
	if !known || has == "" || fits(has, ranges, need.Forge) {
		return avsysv1.UnmetDependency{}, false
	}
	return avsysv1.UnmetDependency{ID: id, Wants: strings.Join(ranges, " or "), Has: has, By: by}, true
}

// metaOf is what a provider's file says it is, read once it is asked for.
func (l *depLister) metaOf(p *provider) *modMeta {
	if p.meta != nil || p.found != nil {
		return p.meta
	}
	m := l.spec.Mods[p.index]
	kind := kindOf(m)
	fileURL := ""
	switch m.Source {
	case avsysv1.SourceModrinth:
		if l.infos[p.index] != nil {
			fileURL = l.infos[p.index].file()
		}
	case avsysv1.SourceURL:
		fileURL = m.Project
	case avsysv1.SourceUpload:
		if files := l.opts.onServer.reach(); files != nil {
			if meta, ok := metaOfServerFile(files, uploadPath(m), kind, ""); ok {
				p.meta = &meta
			}
		}
		return p.meta
	}
	if fileURL != "" {
		if info, err := l.s.inspectURL(l.ctx, fileURL); err == nil {
			meta := info.of(kind)
			p.meta = &meta
		}
	}
	return p.meta
}

// uploadPath is where an upload's file is in a server's files.
func uploadPath(m avsysv1.ContentRef) string {
	if keptInPlace(m) {
		return path.Join(dataRoot, m.File)
	}
	return path.Join(controller.ContentDir, path.Base(m.Project))
}

// entryName is what an entry is called, as its Content page shows it.
func entryName(m avsysv1.ContentRef) string {
	if m.Source == avsysv1.SourceUpload {
		return withoutExt(cmpOr(m.Title, m.Project))
	}
	return cmpOr(m.Title, m.Project)
}

func foundName(f FoundFile) string {
	return cmpOr(f.Title, withoutExt(path.Base(f.Path)))
}

// dependenciesFor lists what a server's content needs, as its list is
// changed from previous (nil for a new server) to spec. A list whose
// content is as it was keeps what was worked out for it, without asking
// Modrinth. One the panel has not listed dependencies for yet is left to
// its image as long as its content is as it was: it is listed with the
// server's files, as the image has them (migrateDependencies). Its content
// changed, it is listed as any other, and the next start gets what that
// says, as the image would have downloaded what it needs anew.
func (s *Server) dependenciesFor(ctx context.Context, spec, previous *avsysv1.MinecraftServerSpec, on *serverContent) error {
	if !controller.ListsDependencies(spec) {
		spec.DependenciesListed = false
		return nil
	}
	legacy := previous != nil && controller.ListsDependencies(previous) && !previous.DependenciesListed
	if legacy && sameContent(spec, previous) {
		spec.DependenciesListed = false
		return nil
	}
	if previous != nil && previous.DependenciesListed && sameContent(spec, previous) {
		spec.DependenciesListed = true
		return nil
	}
	err := s.listDependencies(ctx, spec, depOptions{add: true, onServer: on})
	if err == nil {
		return nil
	}
	// What is needed is not to be had: said as it is.
	var said *Error
	if errors.As(err, &said) && said.Status == http.StatusBadRequest {
		return err
	}
	// Modrinth is not answering, or not about everything.
	switch {
	case previous == nil && spec.DependenciesListed:
		// A copy of a server, listed as the other is: what it was noted to
		// need stays, but what nothing needs any more.
		pruneByNotes(spec)
	case previous == nil || legacy:
		// A new server, or one not listed before: its image downloads what
		// its content needs, as before the panel listed it, until it is
		// listed.
		spec.DependenciesListed = false
	case newModrinth(spec, previous):
		// Content that is new needs Modrinth to be pinned anyway.
		return err
	default:
		// What was listed stays, but what nothing needs any more, as was
		// noted when it was.
		spec.DependenciesListed = true
		pruneByNotes(spec)
	}
	s.Log.Warn("could not work out what a server's content needs; keeping what was listed", "error", err)
	return nil
}

// keepDependencyNotes carries what the panel noted of each entry, from the
// list before, over to the same entry in the list now: what its file
// provides, whether it was added as a dependency, and what needs it. An
// entry changed to another version is someone's own, and its file is
// another. What a request says of these for an entry that was there is
// not taken; one new to the list, as one copied from another server, keeps
// what it says.
func keepDependencyNotes(spec, previous *avsysv1.MinecraftServerSpec) {
	if previous == nil {
		return
	}
	same := map[string]avsysv1.ContentRef{}
	projects := map[string]bool{}
	for _, m := range previous.Mods {
		same[contentKey(m)] = m
		projects[string(m.Source)+":"+m.Project] = true
	}
	for i := range spec.Mods {
		m := &spec.Mods[i]
		if was, ok := same[contentKey(*m)]; ok {
			m.Provides, m.Dependency, m.NeededBy, m.Unmet = was.Provides, was.Dependency, was.NeededBy, was.Unmet
			continue
		}
		if projects[string(m.Source)+":"+m.Project] {
			m.Provides, m.Dependency, m.NeededBy, m.Unmet = nil, false, nil, nil
		}
	}
}

// sameContent says two lists have the same content for the same game: the
// same entries, of the same versions.
func sameContent(a, b *avsysv1.MinecraftServerSpec) bool {
	keys := func(spec *avsysv1.MinecraftServerSpec) []string {
		var out []string
		for _, m := range spec.Mods {
			out = append(out, string(kindOf(m))+" "+contentKey(m)+" "+m.File)
		}
		sort.Strings(out)
		return out
	}
	return targetOf(a) == targetOf(b) && slices.Equal(keys(a), keys(b))
}

// newModrinth says a list has a Modrinth entry, or a version of one, that
// the one before did not.
func newModrinth(spec, previous *avsysv1.MinecraftServerSpec) bool {
	had := map[string]bool{}
	for _, m := range previous.Mods {
		had[contentKey(m)] = true
	}
	for _, m := range spec.Mods {
		if m.Source == avsysv1.SourceModrinth && !had[contentKey(m)] {
			return true
		}
	}
	return false
}

// pruneByNotes takes off what the panel added as a dependency once nothing
// on the list that was noted as needing it is there any more.
func pruneByNotes(spec *avsysv1.MinecraftServerSpec) {
	for changed := true; changed; {
		changed = false
		listed := map[string]bool{}
		for _, m := range spec.Mods {
			listed[m.Project] = true
		}
		out := spec.Mods[:0:0]
		for _, m := range spec.Mods {
			if m.Dependency && len(m.NeededBy) > 0 && !slices.ContainsFunc(m.NeededBy, func(p string) bool { return listed[p] }) {
				changed = true
				continue
			}
			out = append(out, m)
		}
		spec.Mods = out
	}
}

// contentOn is what a server has in its folders besides its list: what was
// found there, and its files, if they can be reached without waiting.
func (s *Server) contentOn(ctx context.Context, mc *avsysv1.MinecraftServer) *serverContent {
	namespace, name := mc.Namespace, mc.Name
	return &serverContent{found: foundOf(mc), files: func() ServerFiles {
		files, err := s.contentFiles(ctx, namespace, name)
		if err != nil {
			return nil
		}
		return files
	}}
}

// listerOn is a lister for a server's content as it is, to ask what it
// provides; nothing in spec is changed by it.
func (s *Server) listerOn(ctx context.Context, spec *avsysv1.MinecraftServerSpec, on *serverContent) *depLister {
	l := &depLister{s: s, ctx: ctx, spec: spec.DeepCopy(), t: targetOf(spec), opts: depOptions{onServer: on}, reached: map[int]bool{}}
	if l.pinnedVersions() != nil {
		l.infos = make([]*modrinthVersion, len(l.spec.Mods))
	}
	return l
}

// provides says something on the server is what a Modrinth project is, as
// the loader knows it: by the mod ID the version of it the panel would
// pin says it is.
func (l *depLister) provides(project string, kind avsysv1.ContentKind) bool {
	k := kind
	v, err := l.versionOf(project, nil, &k)
	if err != nil || v == nil || k == avsysv1.KindDatapack {
		return false
	}
	info, err := l.inspect(v)
	if err != nil {
		return false
	}
	id := info.of(k).primary()
	return id != "" && l.providerOf(id, k, -1) != nil
}

// foundProvides is what a file found in a server's folders gives it, as
// its file says.
func foundProvides(files ServerFiles, f FoundFile) []string {
	if f.Folder || f.Kind == avsysv1.KindDatapack {
		return nil
	}
	if f.SHA1 != "" {
		if info, ok := inspected.get("sha1:" + f.SHA1); ok {
			return info.of(f.Kind).all()
		}
	}
	if meta, ok := metaOfServerFile(files, path.Join(dataRoot, f.Path), f.Kind, f.SHA1); ok {
		return meta.all()
	}
	return nil
}
