package api

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"net/http"
	"path"
	"strings"

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
// with ".." and writing through links.

// kindImport is a server's files being unpacked from an archive.
const kindImport = "import"

var errImporting = &Error{Status: http.StatusConflict, Code: "importing", Message: "This server's files are being imported. Wait for that to finish."}

const (
	importArchive = dataRoot + "/.avsys/import.archive"
	importStaging = dataRoot + "/.avsys/import"
)

// unpacked unpacks the archive into a staging folder and checks that the
// folder named on the first line of stdin is in it. Then rest runs there.
func unpacked(rest string) string {
	return `set -eu
command -v bsdtar >/dev/null || exit 127
IFS= read -r root || true
IFS= read -r split || true
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
if [ ! -d "$s/$root" ]; then
  rm -rf "$s"
  echo "The archive has no folder $root." >&2
  exit 5
fi
` + rest + `
cd /
rm -rf "$s"
`
}

// serverScript takes the folder as the server's, leaves out what only made
// sense on the old host, and moves the rest into the server directory. A
// world under another level-name is renamed to world, which is the one the
// game is told to open.
var serverScript = unpacked(`cd "$s/$root"
rm -rf logs crash-reports debug .avsys .workspace .cache
rm -f ./*.sh ./*.bat ./*.cmd ./*.command ./*.ps1 ./*.pid
find . -name session.lock -type f -delete
level=$(sed -n 's/^level-name=//p' server.properties 2>/dev/null | tail -n 1 | tr -d '\r')
case $level in
  ''|world|*/*|.*) ;;
  *)
    if [ -d "$level" ] && [ ! -e world ]; then
      mv -- "$level" world
      for dim in nether the_end; do
        if [ -d "${level}_$dim" ] && [ ! -e "world_$dim" ]; then mv -- "${level}_$dim" "world_$dim"; fi
      done
    fi
    ;;
esac
if [ -f server.properties ]; then
  sed -i -E '/^(server-ip|server-port|query\.port|enable-query|enable-rcon|rcon\.port|rcon\.password|level-name)=/d' server.properties
fi
for e in .[!.]* ..?* *; do
  [ -e "$e" ] || [ -L "$e" ] || continue
  if [ -d "$e" ] && [ ! -L "$e" ] && [ -d "` + dataRoot + `/$e" ]; then
    cp -a "$e/." "` + dataRoot + `/$e/"
    rm -rf "$e"
  else
    rm -rf "` + dataRoot + `/$e"
    mv -- "$e" ` + dataRoot + `/
  fi
done`)

// worldScript takes the folder as the server's world. The Nether and the End
// are kept in it, as the game itself and single-player saves keep them, or
// with "split" on the second line in folders of their own, as Paper and its
// relatives do. They are found either way in the archive.
var worldScript = unpacked(`w=` + dataRoot + `/world
src="$s/$root"
find "$src" -name session.lock -type f -delete
name=${src##*/}
parent=${src%/*}
# What is in world already (datapacks added with the server) goes along.
if [ -d "$w" ]; then
  cp -a "$w/." "$src/"
  rm -rf "$w"
fi
mv -- "$src" "$w"
mkdir -p "$s"
for pair in nether:DIM-1 the_end:DIM1; do
  suffix=${pair%%:*}
  dim=${pair#*:}
  if [ "$root" != "" ] && [ -d "$parent/${name}_$suffix/$dim" ] && [ ! -e "$w/$dim" ]; then
    mv -- "$parent/${name}_$suffix/$dim" "$w/$dim"
  fi
  if [ "$split" = split ] && [ -d "$w/$dim" ]; then
    mkdir -p "` + dataRoot + `/world_$suffix"
    mv -- "$w/$dim" "` + dataRoot + `/world_$suffix/$dim"
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
// stopped and has no world yet. With ?world=true the folder is a world, and
// becomes the server's. With ?wait=true it is for a server made a moment
// ago: its volume is waited for.
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
	if mc.Spec.State != avsysv1.StateStopped {
		return &Error{Status: http.StatusConflict, Code: "running", Message: "Stop the server first."}
	}
	if err := halfMoved(mc); err != nil {
		return err
	}
	op, ok := s.ops.begin(a.Namespace, a.Org.Slug, mc.Name, kindImport, "")
	if !ok {
		if running := s.ops.current(a.Namespace, mc.Name); running != nil {
			return heldBy(running.Kind)
		}
		return errBusy
	}
	err = s.importInto(r, w, mc, root, r.URL.Query().Get("world") == "true", op)
	s.finishUnsaved(r.Context(), a.Namespace, mc.Name, kindImport, err)
	if err != nil {
		return err
	}
	s.audit(r.Context(), u, a.Org, "server.import", mc.Name, "")
	fresh, err := s.fresh(r.Context(), mc)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, s.view(fresh))
	return nil
}

func (s *Server) importInto(r *http.Request, w http.ResponseWriter, mc *avsysv1.MinecraftServer, root string, world bool, op *operation) error {
	ctx := r.Context()
	s.setOperation(mc.Namespace, op, "Uploading", 0)
	ws, err := s.importWorkspace(ctx, mc, r.URL.Query().Get("wait") == "true")
	if err != nil {
		s.Log.Warn("workspace unreachable", "server", mc.Name, "error", err)
		return &Error{Status: http.StatusBadGateway, Code: "workspace", Message: "The server's files are not reachable right now. Try again."}
	}
	defer ws.Close()
	if gameIn(ctx, s.Client, mc) {
		return &Error{Status: http.StatusConflict, Code: "running", Message: "Stop the server first."}
	}
	sc, err := sftp.NewClient(ws)
	if err != nil {
		return err
	}
	defer sc.Close()
	if world, err := hasWorld(sc); err != nil {
		return fileError(err)
	} else if world != "" {
		return &Error{Status: http.StatusConflict, Code: "has_world", Message: "This server already has a world."}
	}

	if err := sc.MkdirAll(path.Dir(importArchive)); err != nil {
		return fileError(err)
	}
	f, err := sc.Create(importArchive)
	if err != nil {
		return fileError(err)
	}
	_, err = f.ReadFromWithConcurrency(http.MaxBytesReader(w, r.Body, maxUpload), 0)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = sc.Remove(importArchive)
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return &Error{Status: http.StatusRequestEntityTooLarge, Code: "too_large", Message: "Archives over 2 GB must be copied over SSH."}
		}
		return errBadRequest("The upload did not complete: %v.", err)
	}

	// From here it is seen through even if whoever sent it has gone.
	s.setOperation(mc.Namespace, op, "Unpacking", 0)
	session, err := backup.CommandSession(ws)
	if err != nil {
		return err
	}
	defer session.Close()
	script, split := serverScript, ""
	if world {
		script = worldScript
		if splitsDimensions(mc.Spec.Loader) {
			split = "split"
		}
	}
	session.Stdin = strings.NewReader(root + "\n" + split + "\n")
	var stderr bytes.Buffer
	session.Stderr = &stderr
	if err := session.Run(script); err != nil {
		var exit *ssh.ExitError
		if errors.As(err, &exit) && exit.ExitStatus() == 127 {
			return backup.ErrOldWorkspace
		}
		why := strings.TrimSpace(stderr.String())
		if why == "" {
			why = err.Error()
		}
		return errBadRequest("Could not unpack the archive: %s", strings.TrimPrefix(why, "bsdtar: "))
	}
	return nil
}

// importWorkspace reaches a server's workspace: with wait, one made a moment
// ago, once its volume is there.
func (s *Server) importWorkspace(ctx context.Context, mc *avsysv1.MinecraftServer, wait bool) (*ssh.Client, error) {
	if !wait {
		return s.Gateway.DialWorkspace(ctx, mc.Namespace, mc.Name)
	}
	waitCtx, cancel := context.WithTimeout(ctx, newServerIconWait)
	defer cancel()
	if err := s.ownPod(waitCtx, mc); err != nil {
		return nil, err
	}
	return s.dialWorkspaceWithin(ctx, mc.Namespace, mc.Name, newServerIconWait)
}

// gameIn reports whether a server's pod has its game in it.
func gameIn(ctx context.Context, c client.Reader, mc *avsysv1.MinecraftServer) bool {
	pod := &corev1.Pod{}
	if err := c.Get(ctx, client.ObjectKeyFromObject(mc), pod); err != nil {
		return false
	}
	for _, ct := range pod.Spec.Containers {
		if ct.Name == controller.MinecraftContainer {
			return true
		}
	}
	return false
}

// hasWorld is the first folder in the server directory that holds a world,
// or "".
func hasWorld(sc *sftp.Client) (string, error) {
	entries, err := sc.ReadDir(dataRoot)
	if err != nil {
		return "", err
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if _, err := sc.Stat(path.Join(dataRoot, e.Name(), "level.dat")); err == nil {
			return e.Name(), nil
		} else if !errors.Is(err, fs.ErrNotExist) {
			return "", err
		}
	}
	return "", nil
}
