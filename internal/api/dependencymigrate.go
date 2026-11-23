package api

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"path"
	"slices"
	"sort"
	"strings"

	"sigs.k8s.io/controller-runtime/pkg/client"

	avsysv1 "github.com/GT-Drehmal/avSYS/api/v1alpha1"
)

// A server made before the panel listed what content needs has it
// downloaded by its image as it starts, each at whatever version is newest
// then, and keeps that. Switched over by someone (spec.dependenciesListed
// set through the panel), its list is brought over with its files as they
// are, before the image is told to download nothing itself:
//
//   - What the image installed for the list is read from its record,
//     .modrinth-manifest.json, and each file Modrinth knows by its hash.
//   - What the content needs is worked out as for any list, at the very
//     versions the server has files of; and whatever else the image
//     installed for it is listed too, so that the next start keeps every
//     file it has, and gets none.
//   - Changed in one go with spec.dependenciesListed, which tells the image
//     not to download anything more. A running game is noted as running
//     it already (AnnotationSettled), so it is not said to wait for a
//     restart, as far as all it lists was there.
//
// A server whose own environment says how the image is to download what
// content needs keeps that, and a modpack is the pack's: neither is
// brought over.

// installedDeps are the projects the image installed for a server's list
// that the list does not name.
type installedDeps struct {
	// byProject are the versions of each, by project ID, in the order of
	// the image's record: it can have installed two of one, for two mods
	// that each named another version.
	byProject map[string][]*modrinthVersion
	// at is each version's file, by version ID, under /data.
	at map[string]string
}

// installedByImage reads what the image installed from Modrinth beside
// what a server's list names: the files of its record that are not the
// file of a listed version, each known to Modrinth by its hash.
func (s *Server) installedByImage(ctx context.Context, files ServerFiles, spec *avsysv1.MinecraftServerSpec) (installedDeps, error) {
	out := installedDeps{byProject: map[string][]*modrinthVersion{}, at: map[string]string{}}
	f, err := files.Open(path.Join(dataRoot, ".modrinth-manifest.json"))
	if errors.Is(err, fs.ErrNotExist) {
		return out, nil
	}
	if err != nil {
		return out, err
	}
	raw, err := io.ReadAll(io.LimitReader(f, 8<<20))
	f.Close()
	if err != nil {
		return out, err
	}
	var record struct {
		Files []string `json:"files"`
	}
	if err := json.Unmarshal(raw, &record); err != nil {
		return out, err
	}
	// The listed versions' own files are theirs.
	own := map[string]bool{}
	for _, m := range spec.Mods {
		if m.Source != avsysv1.SourceModrinth || m.Version == "" {
			continue
		}
		v, err := s.modrinthVersion(ctx, m.Version)
		if errors.Is(err, errNotFound) {
			continue
		}
		if err != nil {
			return out, err
		}
		own[v.fileName()] = true
	}
	bySum := map[string]string{}
	sumOf := map[string]string{}
	infos := map[string]jarInfo{}
	for _, p := range record.Files {
		if own[path.Base(p)] || path.IsAbs(p) || p != path.Clean(p) || strings.HasPrefix(p, "../") {
			continue
		}
		sum, info, err := readServerJar(files, path.Join(dataRoot, p))
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return out, err
		}
		bySum[sum] = p
		sumOf[p] = sum
		if info != nil {
			infos[sum] = *info
		}
	}
	if len(bySum) == 0 {
		return out, nil
	}
	sums := make([]string, 0, len(bySum))
	for sum := range bySum {
		sums = append(sums, sum)
	}
	sort.Strings(sums)
	known := map[string]modrinthVersion{}
	for start := 0; start < len(sums); start += 500 {
		chunk := sums[start:min(start+500, len(sums))]
		body, err := s.catalog.post(ctx, "https://api.modrinth.com/v2/version_files", map[string]any{"hashes": chunk, "algorithm": "sha1"})
		if err != nil {
			return out, err
		}
		var versions map[string]modrinthVersion
		if err := json.Unmarshal(body, &versions); err != nil {
			return out, err
		}
		for _, sum := range chunk {
			v, ok := versions[sum]
			if !ok || v.ProjectID == "" {
				continue
			}
			// The file it has is that version's: read already, as it is.
			if info, ok := infos[sum]; ok {
				for _, file := range v.Files {
					if file.Hashes["sha1"] == sum && file.URL != "" {
						inspected.keep(file.URL, info)
					}
				}
			}
			known[sum] = v
		}
	}
	for _, p := range record.Files {
		v, ok := known[sumOf[p]]
		if !ok || out.at[v.ID] != "" {
			continue
		}
		out.byProject[v.ProjectID] = append(out.byProject[v.ProjectID], &v)
		out.at[v.ID] = p
	}
	return out, nil
}

// readServerJar reads a file of a server's whole: its SHA-1, and, for a
// jar, what it is, which is kept by the hash.
func readServerJar(files ServerFiles, full string) (string, *jarInfo, error) {
	in, err := files.Open(full)
	if err != nil {
		return "", nil, err
	}
	defer in.Close()
	tmp, size, err := spool(in, maxJar)
	if err != nil {
		return "", nil, err
	}
	defer tmp.Close()
	h := sha1.New()
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		return "", nil, err
	}
	if _, err := io.Copy(h, tmp); err != nil {
		return "", nil, err
	}
	sum := hex.EncodeToString(h.Sum(nil))
	if !strings.HasSuffix(strings.ToLower(full), ".jar") {
		return sum, nil, nil
	}
	info, err := inspectJar(tmp, size)
	if err != nil {
		return sum, nil, nil
	}
	inspected.keep("sha1:"+sum, info)
	return sum, &info, nil
}

// kindOfInstalled is the kind of content a file the image installed is, by
// its place.
func kindOfInstalled(p string) avsysv1.ContentKind {
	switch {
	case strings.HasPrefix(p, pluginsDir+"/"):
		return avsysv1.KindPlugin
	case strings.Contains(p, "/datapacks/"):
		return avsysv1.KindDatapack
	}
	return avsysv1.KindMod
}

// imageRef is an entry as the image is given it in MODRINTH_PROJECTS.
func imageRef(m avsysv1.ContentRef) string {
	ref := m.Project + ":" + m.Version
	if m.Kind == avsysv1.KindDatapack {
		ref = "datapack:" + ref
	}
	return ref
}

// bringOver lists what the content of a server whose image has downloaded
// it until now needs, in spec, with its files as they are: what needs it
// listed at the very versions the server has, and whatever else the image
// installed, so that the next start has every file it has now. runs says
// it does, all of them: not where the image installed two versions of one
// project, which a list names once, the first of them being listed. It is
// asked for, one server at a time (spec.dependenciesListed set through the
// panel).
func (s *Server) bringOver(ctx context.Context, spec *avsysv1.MinecraftServerSpec, found []FoundFile, files ServerFiles) (runs bool, err error) {
	installed, err := s.installedByImage(ctx, files, spec)
	if err != nil {
		return false, err
	}
	runs = true
	for project, versions := range installed.byProject {
		if len(versions) > 1 {
			s.Log.Info("a server has two versions of a project its content needs: the first is listed", "project", project)
			runs = false
		}
	}
	work := spec.DeepCopy()
	had := map[string]bool{}
	for _, m := range work.Mods {
		had[contentKey(m)] = true
	}
	on := &serverContent{found: found, files: func() ServerFiles { return files }}
	if err := s.listDependencies(ctx, work, depOptions{add: true, installed: installed.byProject, onServer: on}); err != nil {
		return false, err
	}
	// Whatever else the image installed for the list is the list's too.
	listed := map[string]bool{}
	for _, m := range work.Mods {
		if m.Source != avsysv1.SourceModrinth || m.Version == "" {
			continue
		}
		if v, err := s.modrinthVersion(ctx, m.Version); err == nil {
			listed[v.ProjectID] = true
		}
		listed[m.Project] = true
	}
	projects := make([]string, 0, len(installed.byProject))
	for project := range installed.byProject {
		projects = append(projects, project)
	}
	sort.Strings(projects)
	t := targetOf(work)
	extra := false
	for _, project := range projects {
		if listed[project] {
			continue
		}
		v := installed.byProject[project][0]
		m := avsysv1.ContentRef{Source: avsysv1.SourceModrinth, Kind: kindOfInstalled(installed.at[v.ID]), Project: project, Version: v.ID, Dependency: true}
		if err := s.pin(ctx, &m, t); err != nil {
			return false, err
		}
		work.Mods = append(work.Mods, m)
		extra = true
	}
	// What needs those is noted as for the rest.
	if extra {
		if err := s.listDependencies(ctx, work, depOptions{installed: installed.byProject, onServer: on}); err != nil {
			return false, err
		}
	}
	// What was added the server has a file of, as the image put it there;
	// a start while Modrinth does not answer starts with it, until the
	// image has it in its record as an entry of the list's.
	var lines []string
	for _, m := range work.Mods {
		if had[contentKey(m)] || m.Source != avsysv1.SourceModrinth {
			continue
		}
		at, ok := installed.at[m.Version]
		if !ok {
			runs = false
			continue
		}
		lines = append(lines, imageRef(m)+"\t"+at)
	}
	if len(lines) > 0 {
		if err := keepImportedRecord(files, func(was []string) []string { return union(was, lines) }); err != nil {
			return false, err
		}
	}
	work.DependenciesListed = true
	*spec = *work
	return runs, nil
}

// settleOfflineRecord forgets, of the panel's record of what a start while
// Modrinth does not answer starts with, what the image has in its own
// record as an entry it was given, and what is no longer on the list.
func (s *Server) settleOfflineRecord(ctx context.Context, files ServerFiles, key client.ObjectKey) {
	mc := &avsysv1.MinecraftServer{}
	if err := s.Client.Get(ctx, key, mc); err != nil {
		return
	}
	given := map[string]bool{}
	if f, err := files.Open(path.Join(dataRoot, ".modrinth-manifest.json")); err == nil {
		raw, err := io.ReadAll(io.LimitReader(f, 8<<20))
		f.Close()
		var record struct {
			Projects []string `json:"projects"`
		}
		if err == nil && json.Unmarshal(raw, &record) == nil {
			for _, p := range record.Projects {
				given[p] = true
			}
		}
	}
	listed := map[string]bool{}
	for _, m := range mc.Spec.Mods {
		if m.Source == avsysv1.SourceModrinth {
			listed[imageRef(m)] = true
		}
	}
	if err := keepImportedRecord(files, func(lines []string) []string {
		return slices.DeleteFunc(slices.Clone(lines), func(line string) bool {
			ref, _, _ := strings.Cut(line, "\t")
			return given[ref] || !listed[ref]
		})
	}); err != nil {
		s.Log.Warn("could not update the record of what was taken from Modrinth", "server", key.Name, "error", err)
	}
}
