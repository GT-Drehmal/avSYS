package api

import (
	"bufio"
	"context"
	"crypto/sha1"
	"encoding/hex"
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
//   - Any other mod, plugin or datapack becomes an upload kept where it is,
//     which the image is not handed.
//
// Nothing changes for the game: a running server is not asked to restart
// for it (see AnnotationSettled), and files it had are not fetched again.
//
// A file taken in is deleted only when someone takes its entry off the list
// through the panel, or changes it to another version: that is noted then,
// with the file's hash (AnnotationContentRemoved), and the file deleted
// only while it still has that hash. A list changed any other way, by
// kubectl, a restore, a template, deletes nothing: what is no longer on it
// is found again, and taken in again.

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

// importedFile is a file taken into a server's list from its folders: the
// entry it is, by key, and its hash then.
type importedFile struct {
	Key  string `json:"key"`
	SHA1 string `json:"sha1,omitempty"`
}

// importedOf is what a server has had taken into its list from its
// folders, by the file's path under /data. A note from before hashes were
// kept has none, and its file is never deleted for it.
func importedOf(mc *avsysv1.MinecraftServer) map[string]importedFile {
	raw := map[string]json.RawMessage{}
	_ = json.Unmarshal([]byte(mc.Annotations[avsysv1.AnnotationImported]), &raw)
	out := map[string]importedFile{}
	for p, v := range raw {
		var f importedFile
		if json.Unmarshal(v, &f) != nil {
			var key string
			if json.Unmarshal(v, &key) != nil {
				continue
			}
			f = importedFile{Key: key}
		}
		out[p] = f
	}
	return out
}

func setImported(mc *avsysv1.MinecraftServer, imported map[string]importedFile) {
	if mc.Annotations == nil {
		mc.Annotations = map[string]string{}
	}
	if len(imported) == 0 {
		delete(mc.Annotations, avsysv1.AnnotationImported)
		return
	}
	raw, _ := json.Marshal(imported)
	mc.Annotations[avsysv1.AnnotationImported] = string(raw)
}

// contentRemoval is a file to delete because someone took its entry off a
// server's list through the panel: deleted only while it has this hash.
type contentRemoval struct {
	Path string `json:"path"`
	SHA1 string `json:"sha1"`
}

func removalsOf(mc *avsysv1.MinecraftServer) []contentRemoval {
	var out []contentRemoval
	_ = json.Unmarshal([]byte(mc.Annotations[avsysv1.AnnotationContentRemoved]), &out)
	return out
}

func setRemovals(mc *avsysv1.MinecraftServer, removals []contentRemoval) {
	if mc.Annotations == nil {
		mc.Annotations = map[string]string{}
	}
	if len(removals) == 0 {
		delete(mc.Annotations, avsysv1.AnnotationContentRemoved)
		return
	}
	raw, _ := json.Marshal(removals)
	mc.Annotations[avsysv1.AnnotationContentRemoved] = string(raw)
}

// noteRemovals notes, on a server whose list someone is changing through
// the panel from before to what mc says now, the files taken into its list
// that are to go: those whose entries are taken off, or changed to another
// version. It is written with the change itself.
func noteRemovals(mc *avsysv1.MinecraftServer, before *avsysv1.MinecraftServerSpec) {
	imported := importedOf(mc)
	if len(imported) == 0 {
		return
	}
	was, now := map[string]bool{}, map[string]bool{}
	for _, m := range before.Mods {
		was[contentKey(m)] = true
	}
	for _, m := range mc.Spec.Mods {
		now[contentKey(m)] = true
	}
	removals := removalsOf(mc)
	for p, f := range imported {
		if !was[f.Key] || now[f.Key] {
			continue
		}
		delete(imported, p)
		if f.SHA1 != "" {
			removals = append(removals, contentRemoval{Path: p, SHA1: f.SHA1})
		}
	}
	setImported(mc, imported)
	setRemovals(mc, removals)
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
		if f.Folder || !f.read() || f.Project == "" || f.VersionID == "" || f.Filename != path.Base(f.Path) || listed[f.Project] {
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
		paths   = map[string]importedFile{}
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
			paths[f.Path] = importedFile{Key: contentKey(m), SHA1: f.SHA1}
			record = append(record, ref+"\t"+f.Path)
			continue
		}
		// Another version of a project the list has: a second copy of it,
		// which is not listed as well. It shows, to be removed.
		if f.Project != "" && listed[f.Project] {
			left = append(left, f)
			continue
		}
		m, ok := importAsUpload(f)
		if !ok {
			left = append(left, f)
			continue
		}
		sum := f.SHA1
		if f.Folder {
			sum, _ = contentHash(files, path.Join(dataRoot, f.Path))
		}
		entries = append(entries, m)
		paths[f.Path] = importedFile{Key: contentKey(m), SHA1: sum}
	}
	if len(entries) == 0 {
		return left, nil
	}
	if len(record) > 0 {
		if err := keepImportedRecord(files, func(lines []string) []string { return union(lines, record) }); err != nil {
			s.Log.Warn("could not note what was taken from Modrinth", "server", key.Name, "error", err)
		}
	}
	if err := s.listImported(ctx, key, entries, paths, left); err != nil {
		return found, err
	}
	return left, nil
}

// importAsUpload makes a found file an upload kept where it is: the entry
// notes its place (File), and the image is not handed it, so nothing is
// copied, and no copy can ever be put over it. One not read whole is left
// for the next look.
func importAsUpload(f FoundFile) (avsysv1.ContentRef, bool) {
	name := path.Base(f.Path)
	m := avsysv1.ContentRef{Source: avsysv1.SourceUpload, Kind: f.Kind, Project: name, File: f.Path, Title: f.Title, VersionName: f.Version, Java: f.Java}
	if f.Kind == avsysv1.KindDatapack {
		m.Description = f.About
	}
	if f.Folder {
		return m, f.Kind == avsysv1.KindDatapack && datapackFolder(name)
	}
	if !f.read() {
		return m, false
	}
	safe, err := contentFilename(name, f.Kind)
	return m, err == nil && safe == name
}

// datapackFolder says a name is one a datapack that is a folder may be
// listed under: a plain name, safe in the image's lists, without .zip.
func datapackFolder(name string) bool {
	return plainName(name) && !strings.HasPrefix(name, ".") && !unsafeFilenameChars.MatchString(name) &&
		strings.TrimSpace(name) == name && !strings.HasSuffix(strings.ToLower(name), ".zip") && len(name) <= 200
}

// keptInPlace says an entry is an upload kept where it was found, at File
// under /data, rather than with the uploads: the image is not handed it.
func keptInPlace(m avsysv1.ContentRef) bool {
	return m.Source == avsysv1.SourceUpload && m.File != ""
}

// inPlaceFile checks where an upload kept in place is: right in mods/ or
// plugins/ for its kind, or in a world's datapacks folder, under its own
// name.
func inPlaceFile(m avsysv1.ContentRef) bool {
	if m.File != path.Clean(m.File) || path.IsAbs(m.File) || path.Base(m.File) != m.Project {
		return false
	}
	dir := path.Dir(m.File)
	switch m.Kind {
	case avsysv1.KindMod:
		return dir == modsDir
	case avsysv1.KindPlugin:
		return dir == pluginsDir
	case avsysv1.KindDatapack:
		world, ok := strings.CutSuffix(dir, "/datapacks")
		return ok && plainName(world) && !strings.HasPrefix(world, ".")
	}
	return false
}

// listImported adds what was taken from the folders to a server's list,
// and notes the files it is in. A server whose game runs, with nothing else
// waiting for a restart, is noted as running what it lists now: it has the
// files already.
func (s *Server) listImported(ctx context.Context, key client.ObjectKey, entries []avsysv1.ContentRef, paths map[string]importedFile, left []FoundFile) error {
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
		imported := importedOf(mc)
		for p, f := range paths {
			imported[p] = f
		}
		setImported(mc, imported)
		// What is listed now is not found besides, in the same change: a
		// page never shows a file twice.
		if len(left) == 0 {
			delete(mc.Annotations, avsysv1.AnnotationFoundContent)
		} else if raw, err := json.Marshal(left); err == nil {
			mc.Annotations[avsysv1.AnnotationFoundContent] = string(raw)
		}
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

// settleImported forgets, of what was taken into a server's list from its
// folders, what is the image's own now, installed by it, and what is no
// longer on the list, however it was taken off: nothing is deleted for it.
// A file that is still there is found again.
func (s *Server) settleImported(ctx context.Context, files ServerFiles, key client.ObjectKey) {
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
	var done []string
	for p, f := range imported {
		if !keys[f.Key] || managed[path.Join(dataRoot, p)] {
			done = append(done, p)
		}
	}
	if len(done) == 0 {
		return
	}
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
		setImported(mc, imported)
		return s.Client.Update(ctx, mc)
	})
}

// finishRemovals deletes the files someone took off a server's list through
// the panel, each only while it has the hash noted then: one changed since
// is left, to be found again. What could not be reached is tried again.
func (s *Server) finishRemovals(ctx context.Context, files ServerFiles, key client.ObjectKey) {
	mc := &avsysv1.MinecraftServer{}
	if err := s.Client.Get(ctx, key, mc); err != nil {
		return
	}
	removals := removalsOf(mc)
	if len(removals) == 0 {
		return
	}
	done := map[string]bool{}
	for _, r := range removals {
		full := path.Join(dataRoot, r.Path)
		if !foundPlace(FoundFile{Path: r.Path, Kind: kindOfPlace(r.Path)}) {
			done[r.Path] = true
			continue
		}
		sum, err := contentHash(files, full)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			done[r.Path] = true
		case err != nil:
			s.Log.Warn("could not read a file taken off a server's list", "server", key.Name, "file", r.Path, "error", err)
		case sum != r.SHA1:
			// Not the file that was taken off: it stays, and is found again.
			done[r.Path] = true
		default:
			remove := files.Remove
			if info, err := files.Stat(full); err == nil && info.IsDir() {
				remove = files.RemoveAll
			}
			if err := remove(full); err != nil && !errors.Is(err, fs.ErrNotExist) {
				s.Log.Warn("could not delete a file taken off a server's list", "server", key.Name, "file", r.Path, "error", err)
				continue
			}
			done[r.Path] = true
		}
	}
	if len(done) == 0 {
		return
	}
	// Nor is a server started with them while Modrinth does not answer.
	if err := keepImportedRecord(files, func(lines []string) []string {
		return slices.DeleteFunc(lines, func(line string) bool {
			_, file, _ := strings.Cut(line, "\t")
			return done[file]
		})
	}); err != nil {
		s.Log.Warn("could not update the record of what was taken from Modrinth", "server", key.Name, "error", err)
	}
	_ = retry.RetryOnConflict(retry.DefaultRetry, func() error {
		mc := &avsysv1.MinecraftServer{}
		if err := s.Client.Get(ctx, key, mc); err != nil {
			return client.IgnoreNotFound(err)
		}
		setRemovals(mc, slices.DeleteFunc(removalsOf(mc), func(r contentRemoval) bool { return done[r.Path] }))
		return s.Client.Update(ctx, mc)
	})
}

// kindOfPlace is the kind of content a place under /data holds.
func kindOfPlace(p string) avsysv1.ContentKind {
	switch path.Dir(p) {
	case modsDir:
		return avsysv1.KindMod
	case pluginsDir:
		return avsysv1.KindPlugin
	}
	return avsysv1.KindDatapack
}

// maxHashed bounds how much of a datapack's folder is read for its hash.
const maxHashed = 20000

// contentHash is the SHA-1 of a file, or for a folder, of each file in it
// by its path and SHA-1.
func contentHash(files ServerFiles, full string) (string, error) {
	info, err := files.Stat(full)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return fileHash(files, full)
	}
	var lines []string
	var walk func(dir, rel string) error
	walk = func(dir, rel string) error {
		entries, err := files.ReadDir(dir)
		if err != nil {
			return err
		}
		for _, e := range entries {
			if len(lines) >= maxHashed {
				return errors.New("too many files to hash")
			}
			p, r := path.Join(dir, e.Name()), path.Join(rel, e.Name())
			if e.IsDir() {
				if err := walk(p, r); err != nil {
					return err
				}
				continue
			}
			sum, err := fileHash(files, p)
			if err != nil {
				return err
			}
			lines = append(lines, r+"\t"+sum)
		}
		return nil
	}
	if err := walk(full, ""); err != nil {
		return "", err
	}
	sort.Strings(lines)
	h := sha1.Sum([]byte(strings.Join(lines, "\n")))
	return hex.EncodeToString(h[:]), nil
}

func fileHash(files ServerFiles, full string) (string, error) {
	f, err := files.Open(full)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha1.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
