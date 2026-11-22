package api

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"slices"
	"sort"
	"strings"

	"sigs.k8s.io/controller-runtime/pkg/client"

	avsysv1 "github.com/GT-Drehmal/avSYS/api/v1alpha1"
	"github.com/GT-Drehmal/avSYS/internal/controller"
)

// A server made before the panel listed what content needs has had it
// downloaded by its image as it starts, each at whatever version was
// newest then. Its list is brought over once, with its files as they are,
// before the image is told to download nothing itself:
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

// recordKey tells the image's record of what it installed from Modrinth as
// it is now from as it was: its size and when it was written. "" where
// there is none.
func recordKey(files ServerFiles) string {
	info, err := files.Stat(path.Join(dataRoot, ".modrinth-manifest.json"))
	if err != nil {
		return ""
	}
	return fmt.Sprintf("%d@%d", info.Size(), info.ModTime().UnixNano())
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

// migrateDependencies brings a server whose image has downloaded what its
// content needs over to the panel listing it, keeping its files as they
// are. It does nothing for one already brought over, one whose own
// environment says how the image is to, or a modpack, nor while its game
// starts or stops or its files are being put in place.
func (s *Server) migrateDependencies(ctx context.Context, key client.ObjectKey, files ServerFiles) error {
	defer s.lockContent(key)()
	mc := &avsysv1.MinecraftServer{}
	if err := s.Client.Get(ctx, key, mc); err != nil {
		return client.IgnoreNotFound(err)
	}
	if mc.Spec.DependenciesListed || !controller.ListsDependencies(&mc.Spec) || !importNow(mc) || s.filesInFlux(mc) {
		return nil
	}
	// Looked at already, as its image's record is now, and left as it is.
	st := s.foundStateOf(key)
	record := recordKey(files)
	st.mu.Lock()
	kept := record != "" && st.depsKept == record
	st.mu.Unlock()
	if kept {
		return nil
	}
	podUID := s.podUIDOf(ctx, key)
	installed, err := s.installedByImage(ctx, files, &mc.Spec)
	if err != nil {
		return err
	}
	// Two versions of one project, as the image installs for two mods that
	// each name another version of it, cannot both be listed: the image
	// takes a project once. Such a server is left to its image, as it was,
	// and listed once its content is changed.
	for project, versions := range installed.byProject {
		if len(versions) > 1 {
			s.Log.Info("a server has two versions of a project its content needs: left to its image until its content is changed", "server", key.Name, "project", project)
			st.mu.Lock()
			st.depsKept = record
			st.mu.Unlock()
			return nil
		}
	}
	spec := mc.Spec.DeepCopy()
	had := map[string]bool{}
	for _, m := range spec.Mods {
		had[contentKey(m)] = true
	}
	on := &serverContent{found: foundOf(mc), files: func() ServerFiles { return files }}
	if err := s.listDependencies(ctx, spec, depOptions{add: true, installed: installed.byProject, onServer: on}); err != nil {
		return err
	}
	// Whatever else the image installed for the list is the list's too:
	// the next start has every file it has now.
	listed := map[string]bool{}
	for _, m := range spec.Mods {
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
	t := targetOf(spec)
	extra := false
	for _, project := range projects {
		if listed[project] {
			continue
		}
		v := installed.byProject[project][0]
		m := avsysv1.ContentRef{Source: avsysv1.SourceModrinth, Kind: kindOfInstalled(installed.at[v.ID]), Project: project, Version: v.ID, Dependency: true}
		if err := s.pin(ctx, &m, t); err != nil {
			return err
		}
		spec.Mods = append(spec.Mods, m)
		extra = true
	}
	// What needs those is noted as for the rest.
	if extra {
		if err := s.listDependencies(ctx, spec, depOptions{installed: installed.byProject, onServer: on}); err != nil {
			return err
		}
	}
	// What was added the server has a file of, as the image put it there;
	// a start while Modrinth does not answer starts with it, until the
	// image has it in its record as an entry of the list's.
	runs := true
	var lines []string
	for _, m := range spec.Mods {
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
			return err
		}
	}
	mc.Spec = *spec
	settleWith(mc, podUID, runs)
	was := mc.Generation
	if err := s.Client.Update(ctx, mc); err != nil {
		return err
	}
	s.Log.Info("listed what a server's content needs", "server", key.Name, "added", len(lines), "settled", mc.Annotations[avsysv1.AnnotationSettled] != "")
	return s.settledAfter(ctx, mc, podUID, was)
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
