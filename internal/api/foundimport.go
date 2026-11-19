package api

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net/url"
	"path"
	"slices"
	"sort"
	"strconv"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"

	avsysv1 "github.com/GT-Drehmal/avSYS/api/v1alpha1"
	"github.com/GT-Drehmal/avSYS/internal/controller"
)

// Content found in a server's folders is taken into its list, as if it had
// been added there, without a file being changed:
//
//   - A file Modrinth has, under the very name Modrinth gives it, becomes
//     that project pinned to that version. The image, given it, finds the
//     file there with the right hash and keeps it. Each project that version
//     needs must be in the list too, or the image would fetch another copy
//     of it: a mod that needs one that is not stays as below.
//   - Any other mod, plugin or datapack becomes an upload: the file is
//     copied to where uploads are kept, and the image, given it, copies it
//     over itself. A datapack that is a folder is kept where it is, which is
//     where an upload of it would be.
//
// Nothing changes for the game: a running server is not asked to restart
// for it (see AnnotationSettled), and files it had are not fetched again.

// importedRecord is the panel's record, in the server's files, of what it
// took into the list from Modrinth that the image has not installed yet:
// a line "ref<TAB>file" each, ref as the image is given it and the file
// relative to /data. A server starting while Modrinth does not answer
// starts with them (see controller.keepContent).
const importedRecord = controller.ImportedRecord

// contentKey names an entry of a server's content, as what was imported is
// noted by.
func contentKey(m avsysv1.ContentRef) string {
	switch m.Source {
	case avsysv1.SourceModrinth, avsysv1.SourceCurseForge:
		return string(m.Source) + ":" + m.Project + "@" + m.Version
	}
	return string(m.Source) + ":" + string(m.Kind) + "/" + m.Project
}

// importedOf is what a server has had taken into its list from its
// folders, by the file's path under /data, to the entry's key.
func importedOf(mc *avsysv1.MinecraftServer) map[string]string {
	out := map[string]string{}
	_ = json.Unmarshal([]byte(mc.Annotations[avsysv1.AnnotationImported]), &out)
	return out
}

// importNow says whether what is in a server's folders may be taken into
// its list now: not while its game is starting or stopping, when the image
// may be putting files there or taking them away.
func importNow(mc *avsysv1.MinecraftServer) bool {
	switch mc.Status.Phase {
	case avsysv1.PhaseStarting, avsysv1.PhaseStopping:
		return false
	}
	return true
}

// importFound takes what was found in a server's folders into its list, as
// far as it can be, and returns what is left.
func (s *Server) importFound(ctx context.Context, key client.ObjectKey, files ServerFiles, found []FoundFile) ([]FoundFile, error) {
	if len(found) == 0 {
		return found, nil
	}
	defer s.lockContent(key)()
	mc := &avsysv1.MinecraftServer{}
	if err := s.Client.Get(ctx, key, mc); err != nil {
		return found, err
	}
	if !importNow(mc) || s.filesInFlux(mc) {
		return found, nil
	}

	// The Modrinth projects the list has, by ID, and those of the files
	// Modrinth has under its own name, each project once.
	listed := map[string]bool{}
	for _, m := range mc.Spec.Mods {
		if m.Source == avsysv1.SourceModrinth {
			listed[s.modrinthID(ctx, m.Project)] = true
		}
	}
	candidates := map[string]FoundFile{}
	for _, f := range found {
		if f.Folder || f.Project == "" || f.VersionID == "" || f.Filename != path.Base(f.Path) || listed[f.Project] {
			continue
		}
		if _, twice := candidates[f.Project]; !twice {
			candidates[f.Project] = f
		}
	}
	// Each is taken from Modrinth only if all it needs is in the list too,
	// which one taken out may change for another.
	for changed := true; changed; {
		changed = false
		for project, f := range candidates {
			for _, need := range f.Requires {
				if _, ok := candidates[need]; !ok && !listed[need] {
					delete(candidates, project)
					changed = true
					break
				}
			}
		}
	}

	var (
		entries []avsysv1.ContentRef
		paths   = map[string]string{}
		record  []string
		left    []FoundFile
	)
	for _, f := range found {
		if c, ok := candidates[f.Project]; ok && c.Path == f.Path {
			m := avsysv1.ContentRef{Source: avsysv1.SourceModrinth, Kind: f.Kind, Project: f.Project, Version: f.VersionID, VersionName: f.Version, Title: f.Title, IconURL: f.IconURL, Java: f.Java}
			ref := m.Project + ":" + m.Version
			if f.Kind == avsysv1.KindDatapack {
				m.File = f.Filename
				ref = "datapack:" + ref
			}
			entries = append(entries, m)
			paths[f.Path] = contentKey(m)
			record = append(record, ref+"\t"+f.Path)
			continue
		}
		// Another version of a project the list has: a second copy of it,
		// which is not listed as well. It shows, to be removed.
		if f.Project != "" && listed[f.Project] {
			left = append(left, f)
			continue
		}
		m, ok := s.importAsUpload(files, f)
		if !ok {
			left = append(left, f)
			continue
		}
		entries = append(entries, m)
		paths[f.Path] = contentKey(m)
	}
	if len(entries) == 0 {
		return left, nil
	}
	if len(record) > 0 {
		if err := keepImportedRecord(files, func(lines []string) []string { return union(lines, record) }); err != nil {
			s.Log.Warn("could not note what was taken from Modrinth", "server", key.Name, "error", err)
		}
	}
	if err := s.listImported(ctx, key, entries, paths); err != nil {
		return found, err
	}
	return left, nil
}

// importAsUpload makes a found file an upload: copied to where uploads are
// kept, or, for a datapack that is a folder, left where it is.
func (s *Server) importAsUpload(files ServerFiles, f FoundFile) (avsysv1.ContentRef, bool) {
	name := path.Base(f.Path)
	m := avsysv1.ContentRef{Source: avsysv1.SourceUpload, Kind: f.Kind, Project: name, Title: f.Title, VersionName: f.Version, Java: f.Java}
	if f.Folder {
		return m, f.Kind == avsysv1.KindDatapack && datapackFolder(name)
	}
	if safe, err := contentFilename(name, f.Kind); err != nil || safe != name || f.Size > maxJar {
		return m, false
	}
	if err := copyFile(files, path.Join(dataRoot, f.Path), path.Join(controller.ContentDir, name)); err != nil {
		s.Log.Warn("could not keep a found file as an upload", "file", f.Path, "error", err)
		return m, false
	}
	return m, true
}

// datapackFolder says a name is one a datapack that is a folder may be
// listed under: a plain name, safe in the image's lists, without .zip.
func datapackFolder(name string) bool {
	return plainName(name) && !strings.HasPrefix(name, ".") && !unsafeFilenameChars.MatchString(name) &&
		strings.TrimSpace(name) == name && !strings.HasSuffix(strings.ToLower(name), ".zip") && len(name) <= 200
}

func copyFile(files ServerFiles, from, to string) error {
	in, err := files.Open(from)
	if err != nil {
		return err
	}
	defer in.Close()
	if err := files.MkdirAll(path.Dir(to)); err != nil {
		return err
	}
	out, err := files.Create(to)
	if err != nil {
		return err
	}
	_, err = io.Copy(out, io.LimitReader(in, maxJar+1))
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	return err
}

// listImported adds what was taken from the folders to a server's list,
// and notes the files it is in. A server whose game runs, with nothing else
// waiting for a restart, is noted as running what it lists now: it has the
// files already.
func (s *Server) listImported(ctx context.Context, key client.ObjectKey, entries []avsysv1.ContentRef, paths map[string]string) error {
	pod := &corev1.Pod{}
	podUID := ""
	if err := s.Client.Get(ctx, key, pod); err == nil && pod.DeletionTimestamp.IsZero() {
		podUID = string(pod.UID)
	}
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		mc := &avsysv1.MinecraftServer{}
		if err := s.Client.Get(ctx, key, mc); err != nil {
			return err
		}
		had := map[string]bool{}
		for _, m := range mc.Spec.Mods {
			had[contentKey(m)] = true
			if m.Source == avsysv1.SourceUpload {
				had["name:"+string(m.Kind)+"/"+m.Project] = true
			}
		}
		added := false
		for _, m := range entries {
			if had[contentKey(m)] || (m.Source == avsysv1.SourceUpload && had["name:"+string(m.Kind)+"/"+m.Project]) {
				continue
			}
			mc.Spec.Mods = append(mc.Spec.Mods, m)
			added = true
		}
		if !added {
			return nil
		}
		if mc.Annotations == nil {
			mc.Annotations = map[string]string{}
		}
		imported := importedOf(mc)
		for p, k := range paths {
			imported[p] = k
		}
		raw, _ := json.Marshal(imported)
		mc.Annotations[avsysv1.AnnotationImported] = string(raw)
		// Nothing waited for a restart before: nothing does after. The
		// change is its next generation.
		if podUID != "" && (settledOn(mc, podUID) || (!mc.Status.PendingRestart && mc.Status.ObservedGeneration == mc.Generation)) {
			mc.Annotations[avsysv1.AnnotationSettled] = podUID + "@" + strconv.FormatInt(mc.Generation+1, 10)
		} else {
			delete(mc.Annotations, avsysv1.AnnotationSettled)
		}
		was := mc.Generation
		if err := s.Client.Update(ctx, mc); err != nil {
			return err
		}
		// Where the generation is not counted as it is in a cluster.
		if podUID != "" && mc.Annotations[avsysv1.AnnotationSettled] != "" && mc.Generation != was+1 {
			mc.Annotations[avsysv1.AnnotationSettled] = podUID + "@" + strconv.FormatInt(mc.Generation, 10)
			if err := s.Client.Update(ctx, mc); err != nil && !apierrors.IsConflict(err) {
				return err
			}
		}
		return nil
	})
}

// settledOn says a server is noted as running what it lists now, in this pod.
func settledOn(mc *avsysv1.MinecraftServer, podUID string) bool {
	return mc.Annotations[avsysv1.AnnotationSettled] == podUID+"@"+strconv.FormatInt(mc.Generation, 10)
}

// modrinthID is a Modrinth project's ID, for a slug or an ID; the name as
// it is where Modrinth cannot be asked.
func (s *Server) modrinthID(ctx context.Context, project string) string {
	body, err := s.catalog.get(ctx, "https://api.modrinth.com/v2/project/"+url.PathEscape(project), lookAgainEvery)
	if err != nil {
		return project
	}
	var p struct {
		ID string `json:"id"`
	}
	if json.Unmarshal(body, &p) != nil || p.ID == "" {
		return project
	}
	return p.ID
}

// lookAgainEvery is how long Modrinth's answer about a project is kept.
var lookAgainEvery = lookEvery * 12

// keepImportedRecord changes the record of what was taken from Modrinth.
func keepImportedRecord(files ServerFiles, change func([]string) []string) error {
	var lines []string
	if f, err := files.Open(importedRecord); err == nil {
		sc := bufio.NewScanner(io.LimitReader(f, 1<<20))
		for sc.Scan() {
			if line := strings.TrimSpace(sc.Text()); line != "" {
				lines = append(lines, line)
			}
		}
		f.Close()
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	now := change(lines)
	sort.Strings(now)
	if slices.Equal(now, lines) {
		return nil
	}
	if len(now) == 0 {
		if err := files.Remove(importedRecord); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		return nil
	}
	if err := files.MkdirAll(path.Dir(importedRecord)); err != nil {
		return err
	}
	out, err := files.Create(importedRecord)
	if err != nil {
		return err
	}
	_, err = io.WriteString(out, strings.Join(now, "\n")+"\n")
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	return err
}

// dropImported settles what was taken into a server's list from its
// folders: a file whose entry has gone from the list, or is now another
// version, is deleted, as the image would delete one it had installed, so
// that it is neither found again nor kept beside the version that takes
// its place. One the image has since installed itself is its own, and
// forgotten here.
func (s *Server) dropImported(ctx context.Context, files ServerFiles, key client.ObjectKey) {
	mc := &avsysv1.MinecraftServer{}
	if err := s.Client.Get(ctx, key, mc); err != nil {
		return
	}
	imported := importedOf(mc)
	if len(imported) == 0 {
		return
	}
	keys := map[string]bool{}
	for _, m := range mc.Spec.Mods {
		keys[contentKey(m)] = true
	}
	managed := managedFiles(files, &avsysv1.MinecraftServerSpec{})
	var gone, settled []string
	for p, k := range imported {
		switch {
		case !keys[k]:
			gone = append(gone, p)
		case managed[path.Join(dataRoot, p)]:
			settled = append(settled, p)
		}
	}
	if len(gone)+len(settled) == 0 {
		return
	}
	var deleted []string
	for _, p := range gone {
		full := path.Join(dataRoot, p)
		var err error
		if info, serr := files.Stat(full); serr == nil && info.IsDir() {
			err = files.RemoveAll(full)
		} else {
			err = files.Remove(full)
		}
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			s.Log.Warn("could not delete a file taken off a server's list", "server", key.Name, "file", p, "error", err)
			continue
		}
		deleted = append(deleted, p)
	}
	done := append(deleted, settled...)
	if err := keepImportedRecord(files, func(lines []string) []string {
		return slices.DeleteFunc(lines, func(line string) bool {
			_, file, _ := strings.Cut(line, "\t")
			return slices.Contains(done, file)
		})
	}); err != nil {
		s.Log.Warn("could not update the record of what was taken from Modrinth", "server", key.Name, "error", err)
	}
	_ = retry.RetryOnConflict(retry.DefaultRetry, func() error {
		mc := &avsysv1.MinecraftServer{}
		if err := s.Client.Get(ctx, key, mc); err != nil {
			return client.IgnoreNotFound(err)
		}
		imported := importedOf(mc)
		for _, p := range done {
			delete(imported, p)
		}
		if len(imported) == 0 {
			delete(mc.Annotations, avsysv1.AnnotationImported)
		} else {
			raw, _ := json.Marshal(imported)
			mc.Annotations[avsysv1.AnnotationImported] = string(raw)
		}
		return s.Client.Update(ctx, mc)
	})
}
