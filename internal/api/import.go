package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	avsysv1 "github.com/GT-Drehmal/avSYS/api/v1alpha1"
	"github.com/GT-Drehmal/avSYS/internal/backup"
	"github.com/GT-Drehmal/avSYS/internal/controller"
	"github.com/GT-Drehmal/avSYS/internal/store"
)

// An existing server, from another host or someone's computer, is brought in
// as an archive of its folder. The page reads what it can out of the archive
// to fill in the new server's settings; the panel makes the server stopped,
// and the archive is sent here to be unpacked into it. It is unpacked inside
// the server's workspace, by bsdtar, which refuses absolute paths, paths
// with ".." and writing through links; links that come out of it are
// dropped.
//
// An import goes into a server with no files yet. Like a restore, it has the
// server's pod made again first, which ends any shell open to its files,
// and marks the server while the files are put in: the game is not started
// on a server whose import did not finish, which can be imported into again.

// kindImport is a server's files being unpacked from an archive.
const kindImport = "import"

var (
	errImporting = &Error{Status: http.StatusConflict, Code: "importing", Message: "This server's files are being imported. Wait for that to finish."}
	// errImportIncomplete is returned to anything that would run the game on
	// files an import started on and did not finish.
	errImportIncomplete = &Error{Status: http.StatusConflict, Code: "import_incomplete", Message: "An import did not finish, so files may be missing. Import again, restore a snapshot, or delete the server."}
	errHasFiles         = &Error{Status: http.StatusConflict, Code: "has_files", Message: "This server has files already. Import into a new server."}
)

// importUnfinished reports whether an import started on this server's files
// and never completed.
func importUnfinished(mc *avsysv1.MinecraftServer) bool {
	_, marked := mc.Annotations[avsysv1.AnnotationImporting]
	return marked
}

const (
	importArchive = dataRoot + "/.avsys/import.archive"
	importStaging = dataRoot + "/.avsys/import"
)

// kept are what an import leaves in the server directory: the panel's own
// files, the workspace's home and the volume's own folder.
var kept = []string{".avsys", ".workspace", "lost+found"}

// unpacked unpacks the archive into a staging folder, drops any links that
// came out of it, checks that the folder named on the first line of stdin is
// there, and empties the server directory but for what is kept. Then rest
// runs, and the staging folder is removed. The next lines of stdin say how:
// "split", "listed", "replace" and the world's folder, or nothing. With
// "replace", only the world's folders are emptied, for the one unpacked to
// take their place; the world's folder is "world" unless said.
func unpacked(rest string) string {
	return `set -eu
command -v bsdtar >/dev/null || exit 127
IFS= read -r root || true
IFS= read -r split || true
IFS= read -r listed || true
IFS= read -r replace || true
IFS= read -r level || true
lv=${level:-world}
a=` + importArchive + `
s=` + importStaging + `
rm -rf "$s" "$s.err"
mkdir -p "$s"
if ! bsdtar -xf "$a" -C "$s" 2>"$s.err"; then
  head -n 1 "$s.err" >&2
  rm -rf "$s" "$s.err" "$a"
  exit 4
fi
rm -f "$a" "$s.err"
find "$s" -type l -delete
find "$s" -name session.lock -type f -delete
if [ ! -d "$s/$root" ]; then
  rm -rf "$s"
  echo "The archive has no folder $root." >&2
  exit 5
fi
if [ "$replace" = replace ]; then
  rm -rf "` + dataRoot + `/$lv" "` + dataRoot + `/${lv}_nether" "` + dataRoot + `/${lv}_the_end"
else
  find ` + dataRoot + ` -mindepth 1 -maxdepth 1 ! -name .avsys ! -name .workspace ! -name lost+found -exec rm -rf {} +
fi
` + rest + `
cd /
rm -rf "$s"
`
}

// serverScript takes the folder as the server's, leaves out what only made
// sense on the old host, and moves the rest into the server directory. A
// world under another level-name is renamed to world, which is the one the
// game is told to open; a world folder that was there under that name is
// put aside.
var serverScript = unpacked(`cd "$s/$root"
rm -rf debug .avsys .workspace .cache lost+found
rm -f ./*.sh ./*.bat ./*.cmd ./*.command ./*.ps1 ./*.pid
aside() {
  n=1
  while [ -e "$1.unused$n" ]; do n=$((n + 1)); done
  mv -- "$1" "$1.unused$n"
}
level=$(sed -n 's/^level-name=//p' server.properties 2>/dev/null | tail -n 1 | tr -d '\r')
case $level in
  ''|world|*/*|.*) ;;
  *)
    if [ -d "$level" ]; then
      for dim in '' _nether _the_end; do
        if [ -d "$level$dim" ]; then
          if [ -e "world$dim" ]; then aside "world$dim"; fi
          mv -- "$level$dim" "world$dim"
        fi
      done
    fi
    ;;
esac
if [ "$listed" = listed ]; then rm -f mods/*.jar plugins/*.jar world/datapacks/*.zip datapacks/*.zip; fi
if [ -f server.properties ]; then
  sed -i -E '/^(server-ip|server-port|query\.port|enable-query|enable-rcon|rcon\.port|rcon\.password|level-name)=/d' server.properties
fi
for e in .[!.]* ..?* *; do
  [ -e "$e" ] || continue
  mv -- "$e" ` + dataRoot + `/
done`)

// worldScript takes the folder as the server's world. The Nether and the End
// are kept in it, as the game itself and single-player saves keep them, or
// with "split" on the second line in folders of their own, as Paper and its
// relatives do. They are found either way in the archive, and a folder of
// one of them beside the world goes as a whole.
var worldScript = unpacked(`w=` + dataRoot + `/$lv
src="$s/$root"
name=${src##*/}
parent=${src%/*}
mv -- "$src" "$w"
mkdir -p "$s"
for pair in nether:DIM-1 the_end:DIM1; do
  suffix=${pair%%:*}
  dim=${pair#*:}
  beside="$parent/${name}_$suffix"
  if [ "$root" != "" ] && [ -d "$beside" ]; then
    if [ "$split" = split ] && [ ! -e "$w/$dim" ]; then
      mv -- "$beside" "` + dataRoot + `/${lv}_$suffix"
    elif [ -d "$beside/$dim" ] && [ ! -e "$w/$dim" ]; then
      mv -- "$beside/$dim" "$w/$dim"
    fi
  fi
  if [ "$split" = split ] && [ -d "$w/$dim" ]; then
    mkdir -p "` + dataRoot + `/${lv}_$suffix"
    if [ ! -e "` + dataRoot + `/${lv}_$suffix/$dim" ]; then mv -- "$w/$dim" "` + dataRoot + `/${lv}_$suffix/$dim"; fi
  fi
done`)

// splitsDimensions reports whether a server's software keeps the Nether and
// the End in folders of their own beside the world.
func splitsDimensions(l avsysv1.Loader) bool {
	return l == avsysv1.LoaderPaper || l == avsysv1.LoaderPurpur || l == avsysv1.LoaderFolia
}

// archiveRoot is the folder inside an archive that holds the server, as the
// page found it: "" for the archive's top.
func archiveRoot(p string) (string, error) {
	if p == "" {
		return "", nil
	}
	clean := path.Clean("/" + p)[1:]
	if strings.Contains(p, "\n") || clean != strings.Trim(p, "/") {
		return "", errBadRequest("Not a folder in the archive.")
	}
	return clean, nil
}

// importFiles unpacks an archive of a server's folder into a server that is
// stopped and has no files yet. With ?world=true the folder is a world, and
// becomes the server's. With ?replace=true as well, it takes the place of the
// world of a server that has files, once a snapshot of it is taken.
//
// It answers once the archive is in, and the panel sees the rest through:
// how it went is told as the operation ends. With ?wait=true it answers when
// all is done instead, and is for a server made a moment ago, whose volume is
// waited for.
func (s *Server) importFiles(w http.ResponseWriter, r *http.Request, u *store.User) error {
	wait := r.URL.Query().Get("wait") == "true"
	a, mc, err := s.serverMaybeNew(r, u, wait)
	if err != nil {
		return err
	}
	root, err := archiveRoot(r.URL.Query().Get("root"))
	if err != nil {
		return err
	}
	replace := r.URL.Query().Get("replace") == "true"
	if replace && r.URL.Query().Get("world") != "true" {
		return errBadRequest("Only a world can replace a server's world.")
	}
	if replace && !s.Backups.Available() {
		// What it replaces could not be got back.
		return errNoBackups
	}
	if replace && createUnfinished(mc) {
		return errCreateIncomplete
	}
	if replace && levelOf(&mc.Spec) == "" {
		return errBadRequest("Which folder this server's world is in could not be told from its settings, so it cannot be replaced here.")
	}
	if mc.Spec.State != avsysv1.StateStopped {
		return &Error{Status: http.StatusConflict, Code: "running", Message: "Stop the server first."}
	}
	if err := halfMoved(mc); err != nil {
		return err
	}
	// What only an import can mend is let through.
	if err := unfinished(mc); err != nil && !importUnfinished(mc) {
		return err
	}
	op, ok := s.ops.begin(a.Namespace, a.Org.Slug, mc.Name, kindImport, "")
	if !ok {
		if running := s.ops.current(a.Namespace, mc.Name); running != nil {
			return heldBy(running.Kind)
		}
		return errBusy
	}
	// The work goes on in a goroutine of its own, so that the handler can
	// return, and the answer end, once the archive is in: over HTTP/2 an
	// answer is not over until its handler is.
	received := make(chan struct{})
	finished := make(chan error, 1)
	answered := false
	var onReceived func()
	if !wait {
		onReceived = func() {
			answered = true
			close(received)
		}
	}
	go func() {
		undo, err := s.importInto(r, w, mc, root, op, replace, a.Org.Slug, u.Username, onReceived)
		ctx := context.WithoutCancel(r.Context())
		if undo != nil || answered {
			// What it replaced is in a snapshot, noted on the server as its
			// newest. One answered before it was done says on the server how
			// it went, for whoever missed its end.
			s.finishOperation(ctx, a.Namespace, mc.Name, kindImport, nil, undo, err)
		} else {
			s.finishUnsaved(ctx, a.Namespace, mc.Name, kindImport, err)
		}
		if err == nil {
			if replace {
				s.audit(ctx, u, a.Org, "server.replace-world", mc.Name, "")
			} else {
				s.audit(ctx, u, a.Org, "server.import", mc.Name, "")
			}
		}
		finished <- err
	}()
	select {
	case <-received:
		view := s.view(mc)
		if fresh, err := s.fresh(r.Context(), mc); err == nil {
			view = s.view(fresh)
		}
		writeJSON(w, http.StatusAccepted, view)
		return nil
	case err := <-finished:
		if err != nil {
			return err
		}
	}
	fresh, err := s.fresh(context.WithoutCancel(r.Context()), mc)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, s.view(fresh))
	return nil
}

// importInto returns the snapshot it took of what a world replaced, once there
// is one, whether or not the rest then succeeded. received, if given, is
// called once the archive is in.
func (s *Server) importInto(r *http.Request, w http.ResponseWriter, mc *avsysv1.MinecraftServer, root string, op *operation, replace bool, org, by string, received func()) (undo *backup.Snapshot, err error) {
	ctx := r.Context()
	key := client.ObjectKeyFromObject(mc)
	wasMarked := importUnfinished(mc)
	if r.URL.Query().Get("wait") == "true" {
		waitCtx, cancel := context.WithTimeout(ctx, newServerIconWait)
		err := s.ownPod(waitCtx, mc)
		cancel()
		if err != nil {
			return undo, &Error{Status: http.StatusBadGateway, Code: "workspace", Message: "The server's files are not reachable right now. Try again."}
		}
	}
	// Its pod is made again, so that a shell already open to its files ends,
	// and it is marked: from here the game is not started.
	s.setOperation(mc.Namespace, op, "Getting ready", 0)
	stamp := time.Now().UTC().Format(time.RFC3339Nano)
	mark := func(m *avsysv1.MinecraftServer) { m.Annotations[avsysv1.AnnotationImporting] = "true" }
	if err := s.stopReplacing(ctx, key, stamp, mark); err != nil {
		return undo, err
	}
	// Until the files are touched, an import that goes no further leaves the
	// server as it was.
	touched := false
	defer func() {
		if !touched && !wasMarked {
			if err := s.markImporting(context.WithoutCancel(ctx), key, false); err != nil {
				s.Log.Warn("could not take the import mark off a server", "server", mc.Name, "error", err)
			}
		}
	}()
	if err := s.waitRestarted(ctx, key, stamp, stopWait); err != nil {
		return undo, err
	}
	ws, err := s.dialWorkspaceWithin(ctx, mc.Namespace, mc.Name, 5*time.Minute)
	if err != nil {
		s.Log.Warn("workspace unreachable", "server", mc.Name, "error", err)
		return undo, &Error{Status: http.StatusBadGateway, Code: "workspace", Message: "The server's files are not reachable right now. Try again."}
	}
	defer ws.Close()
	if running, err := gameIn(ctx, s.nodeReader(), mc); err != nil {
		return undo, err
	} else if running {
		return undo, &Error{Status: http.StatusConflict, Code: "running", Message: "Stop the server first."}
	}
	sc, err := sftp.NewClient(ws)
	if err != nil {
		return undo, err
	}
	defer sc.Close()
	// Over a server whose import did not finish, what it left is replaced.
	// A world replaced goes among files that are there.
	if !wasMarked && !replace {
		if has, err := hasFiles(sc); err != nil {
			return undo, fileError(err)
		} else if has {
			return undo, errHasFiles
		}
	}

	if r.URL.Query().Get("from") == "upload" {
		// Sent before in parts, by this upload: all of it, as large as the
		// page said.
		owner, size, _, err := uploadOf(sc)
		if err != nil {
			return undo, fileError(err)
		}
		if owner == "" || owner != r.URL.Query().Get("upload") {
			return undo, errBadRequest("Nothing was uploaded to import.")
		}
		if want := r.URL.Query().Get("size"); want != strconv.FormatInt(size, 10) {
			return undo, errBadRequest("Only %s of the archive arrived. Send it again.", sizeOf(size))
		}
		_ = sc.Remove(importArchive)
		if err := sc.Rename(importUpload, importArchive); err != nil {
			return undo, fileError(err)
		}
		_ = sc.Remove(importUploadID)
	} else {
		s.setOperation(mc.Namespace, op, "Uploading", 0)
		if err := sc.MkdirAll(path.Dir(importArchive)); err != nil {
			return undo, fileError(err)
		}
		f, err := sc.Create(importArchive)
		if err != nil {
			return undo, fileError(err)
		}
		_, err = f.ReadFromWithConcurrency(http.MaxBytesReader(w, r.Body, maxUpload), 0)
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			_ = sc.Remove(importArchive)
			var tooLarge *http.MaxBytesError
			if errors.As(err, &tooLarge) {
				return undo, &Error{Status: http.StatusRequestEntityTooLarge, Code: "too_large", Message: "Archives over 2 GB are sent in parts."}
			}
			return undo, errBadRequest("The upload did not complete: %v.", err)
		}
	}

	// From here it is seen through even if whoever sent it has gone.
	ctx = context.WithoutCancel(ctx)
	if received != nil {
		received()
	}
	if replace {
		// What it replaces is kept first: nothing is touched if it cannot be.
		s.setOperation(mc.Namespace, op, "Saving the current world first", 0)
		undo, err = s.takeSnapshot(ctx, org, mc, op, backup.TriggerPreReplace, by)
		if err != nil {
			_ = sc.Remove(importArchive)
			s.Log.Error("snapshot before replacing a world failed", "server", mc.Name, "error", err)
			return nil, &Error{Status: http.StatusBadGateway, Code: "snapshot_failed", Message: sentence("the current world could not be saved first, so nothing was changed: " + plainly(err))}
		}
		// It is the way back until the world is in: not to be deleted before.
		if !s.ops.carry(mc.Namespace, op, undo.ID) {
			_ = sc.Remove(importArchive)
			return undo, &Error{Status: http.StatusConflict, Code: "snapshot_deleting", Message: "The snapshot just taken of it is being deleted, so nothing was changed."}
		}
	}
	touched = true
	s.setOperation(mc.Namespace, op, "Unpacking", 0)
	session, err := backup.CommandSession(ws)
	if err != nil {
		return undo, err
	}
	defer session.Close()
	script, split := serverScript, ""
	if r.URL.Query().Get("world") == "true" {
		script = worldScript
		if splitsDimensions(mc.Spec.Loader) {
			split = "split"
		}
	}
	// Its mods, plugins and datapacks listed as the server's content are
	// installed from that list, and their files are not kept besides.
	listed := ""
	if r.URL.Query().Get("content") == "listed" {
		listed = "listed"
	}
	again, level := "", ""
	if replace {
		again, level = "replace", levelOf(&mc.Spec)
	}
	session.Stdin = strings.NewReader(root + "\n" + split + "\n" + listed + "\n" + again + "\n" + level + "\n")
	var stderr bytes.Buffer
	session.Stderr = &stderr
	if err := session.Run(script); err != nil {
		var exit *ssh.ExitError
		if errors.As(err, &exit) && exit.ExitStatus() == 127 {
			return undo, backup.ErrOldWorkspace
		}
		why := strings.TrimSpace(stderr.String())
		if why == "" {
			why = err.Error()
		}
		return undo, errBadRequest("Could not unpack the archive: %s", strings.TrimPrefix(why, "bsdtar: "))
	}
	// Its operators and whitelist were read into the server's settings, and
	// may have been changed there before it was made: a player taken out is
	// taken out of its files too, and those kept keep what the files say of
	// them, such as an operator's level.
	if r.URL.Query().Get("players") == "listed" {
		for file, names := range map[string][]string{"ops.json": mc.Spec.Ops, "whitelist.json": mc.Spec.Whitelist} {
			if err := keepListed(sc, path.Join(dataRoot, file), names); err != nil {
				return undo, fileError(err)
			}
		}
	}
	s.serverIcons.forget(mc.Namespace + "/" + mc.Name)
	return undo, s.markImporting(ctx, key, false)
}

// keepListed leaves in a list of players the game keeps, such as ops.json,
// only the players named, by name or UUID. Each keeps its entry as it was.
// A file that cannot be read as such a list goes: the names put it back.
func keepListed(sc *sftp.Client, file string, names []string) error {
	f, err := sc.Open(file)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	data, err := io.ReadAll(io.LimitReader(f, 4<<20))
	f.Close()
	if err != nil {
		return err
	}
	var entries []map[string]any
	if json.Unmarshal(data, &entries) != nil {
		return sc.Remove(file)
	}
	named := func(e map[string]any) bool {
		name, _ := e["name"].(string)
		uuid, _ := e["uuid"].(string)
		for _, n := range names {
			if (name != "" && strings.EqualFold(n, name)) || (uuid != "" && strings.EqualFold(strings.ReplaceAll(n, "-", ""), strings.ReplaceAll(uuid, "-", ""))) {
				return true
			}
		}
		return false
	}
	kept := slices.DeleteFunc(slices.Clone(entries), func(e map[string]any) bool { return !named(e) })
	if len(kept) == len(entries) {
		return nil
	}
	out, err := json.MarshalIndent(kept, "", "  ")
	if err != nil {
		return err
	}
	tmp := file + ".avsys-tmp"
	w, err := sc.Create(tmp)
	if err != nil {
		return err
	}
	_, err = w.Write(out)
	if cerr := w.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = sc.PosixRename(tmp, file)
	}
	if err != nil {
		_ = sc.Remove(tmp)
	}
	return err
}

// markImporting sets or clears the mark of an import on a server.
func (s *Server) markImporting(ctx context.Context, key client.ObjectKey, on bool) error {
	mc := &avsysv1.MinecraftServer{}
	if err := s.Client.Get(ctx, key, mc); err != nil {
		return err
	}
	orig := mc.DeepCopy()
	if on {
		if mc.Annotations == nil {
			mc.Annotations = map[string]string{}
		}
		mc.Annotations[avsysv1.AnnotationImporting] = "true"
	} else {
		delete(mc.Annotations, avsysv1.AnnotationImporting)
	}
	return s.Client.Patch(ctx, mc, client.MergeFrom(orig))
}

// gameIn reports whether a server's pod has its game in it. Not being able to
// tell is an error, not a no.
func gameIn(ctx context.Context, c client.Reader, mc *avsysv1.MinecraftServer) (bool, error) {
	pod := &corev1.Pod{}
	if err := c.Get(ctx, client.ObjectKeyFromObject(mc), pod); err != nil {
		return false, err
	}
	for _, ct := range pod.Spec.Containers {
		if ct.Name == controller.MinecraftContainer {
			return true, nil
		}
	}
	return false, nil
}

// hasFiles reports whether the server directory has anything in it but what
// an import keeps.
func hasFiles(sc *sftp.Client) (bool, error) {
	entries, err := sc.ReadDir(dataRoot)
	if err != nil {
		return false, err
	}
	for _, e := range entries {
		if !slices.Contains(kept, e.Name()) {
			return true, nil
		}
	}
	return false, nil
}
