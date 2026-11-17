package api

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"path"
	"time"

	"golang.org/x/crypto/ssh"

	avsysv1 "github.com/GT-Drehmal/avSYS/api/v1alpha1"
	"github.com/GT-Drehmal/avSYS/internal/store"
)

// A server's world is downloaded as it is now, straight from its files, as a
// zip of a single-player save laid out as one from the library is: the
// world's own files in a folder named after the server, with the Nether and
// the End that a server's software may keep beside it (world_nether/DIM-1,
// world_the_end/DIM1) put back in it as DIM-1 and DIM1.
//
// A running game is told to write everything out and then hold its saves
// while its files are read, as for a snapshot: otherwise the zip could miss
// what the game still holds in memory, or catch a region file half written.
// Saving is switched back on as the download ends, however it ends. While it
// lasts nothing else is done to the server's files: no snapshot, restore or
// import begins, and one under way is waited for first. A game restarted
// meanwhile writes its world again, so a download across a restart is cut
// off, and the browser says it failed.

// worldHoldLimit is how long a download may hold a running game's saves. One
// that takes longer is cut off, and the game saves again: a download left
// paused in the browser does not keep a game from saving for hours.
var worldHoldLimit = time.Hour

// errNoWorldYet is said of a server whose game has not made its world yet.
var errNoWorldYet = &Error{Status: http.StatusNotFound, Code: "no_world", Message: "This server has no world yet. The game makes it when the server first starts."}

// downloadServerWorld sends a server's world as a zip of a single-player
// save, made as it is sent.
func (s *Server) downloadServerWorld(w http.ResponseWriter, r *http.Request, u *store.User) error {
	a, mc, err := s.server(r, u, store.RoleOperator)
	if err != nil {
		return err
	}
	// Files a restore, copy or import is putting in are not sent.
	if err := unfinished(mc); err != nil {
		return err
	}
	if err := settled(mc); err != nil {
		return err
	}
	// Held from operations until it is sent: a snapshot would switch saving
	// back on as it ended, and a restore or import would change the files
	// under it.
	release, err := s.changing(mc, true)
	if err != nil {
		return err
	}
	defer release()
	ctx := r.Context()
	// As it is now: it may have been started or stopped since it was read.
	if mc, err = s.fresh(ctx, mc); err != nil {
		return err
	}
	if err := settled(mc); err != nil {
		return err
	}
	if s.Gateway == nil {
		return s.unreachable(mc.Name, errNoWorkspaces)
	}
	ws, err := s.Gateway.DialWorkspace(ctx, a.Namespace, mc.Name)
	if err != nil {
		return s.unreachable(mc.Name, err)
	}
	defer ws.Close()
	level, err := s.levelIn(ctx, ws, mc)
	if err != nil {
		return err
	}
	if err := runIn(ws, "test -d "+shellQuote(path.Join(dataRoot, level))); err != nil {
		var exit *ssh.ExitError
		if errors.As(err, &exit) {
			return errNoWorldYet
		}
		return s.unreachable(mc.Name, err)
	}
	if mc.Status.Phase == avsysv1.PhaseRunning {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, worldHoldLimit)
		defer cancel()
		hold, _, err := s.holdSaves(ctx, mc, ws)
		if err != nil {
			// A game that does not answer is not saving either: its files
			// are sent as they are.
			s.Log.Warn("a world is downloaded without the game's saves held", "server", mc.Name, "error", err)
		} else {
			defer hold()
		}
	}
	before := s.gameInstance(ctx, mc)
	name := saveName(displayNameOf(mc))
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", "attachment; filename*=UTF-8''"+url.PathEscape(name+" world.zip"))
	sent := &countWriter{}
	err = s.writeServerWorldZip(ctx, ws, level, name, io.MultiWriter(w, sent), func() error {
		if s.gameInstance(context.WithoutCancel(ctx), mc) != before {
			return errors.New("the game restarted while its world was read")
		}
		return nil
	})
	if err != nil {
		s.Log.Warn("a server's world download did not finish", "server", mc.Name, "error", err)
		// Nothing sent yet: said so. Once it has begun, the download is cut
		// off, so that the browser says it failed rather than keep a zip
		// without some of the world.
		if sent.n == 0 {
			w.Header().Del("Content-Disposition")
			w.Header().Del("Content-Type")
			if errors.Is(err, errNoWorld) {
				return errNoWorldYet
			}
			return &Error{Status: http.StatusBadGateway, Code: "workspace", Message: "The server's world could not be read."}
		}
		panic(http.ErrAbortHandler)
	}
	return nil
}

// levelIn is the server's world's folder, as its files say: as its
// server.properties names it, else as its settings do.
func (s *Server) levelIn(ctx context.Context, ws *ssh.Client, mc *avsysv1.MinecraftServer) (string, error) {
	var props bytes.Buffer
	if err := s.runIn(ctx, ws, "cat "+shellQuote(path.Join(dataRoot, "server.properties"))+" 2>/dev/null || true", &props); err != nil {
		return "", s.unreachable(mc.Name, err)
	}
	return worldLevel(levelNamed(&props), levelOf(&mc.Spec)), nil
}

// errNoWorld is a world's folder that is not there.
var errNoWorld = errors.New("no world")

// worldTar is the command that writes a server's world, its Nether and End
// beside it included where it has them, to stdout as a tar, by their paths
// from the server's folder.
func worldTar(level string) string {
	q := func(dir string) string { return shellQuote("./" + dir) }
	// tar's status 1 is a file changed as it was read, which is all a game
	// that would not hold its saves can be read as.
	return "cd " + shellQuote(dataRoot) + " && set -- " + q(level) +
		"; for d in " + q(level+"_nether") + " " + q(level+"_the_end") + `; do if [ -d "$d" ]; then set -- "$@" "$d"; fi; done` +
		`; tar -cf - "$@"; s=$?; [ "$s" -le 1 ] || exit "$s"`
}

// writeServerWorldZip writes the world in a server's folder level to out as a
// zip of a single-player save in the folder name, read from its files as it
// is written. check is asked once all of it is read, before the zip is
// finished: an error there leaves the zip unfinished.
func (s *Server) writeServerWorldZip(ctx context.Context, ws *ssh.Client, level, name string, out io.Writer, check func() error) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	pr, pw := io.Pipe()
	read := make(chan error, 1)
	go func() {
		err := s.runIn(ctx, ws, worldTar(level), pw)
		pw.CloseWithError(err)
		read <- err
	}()
	zw := saveZip(out)
	written, err := zipWorld(zw, pr, level, name, nil)
	if err == nil {
		// The rest of the tar, after its end, and how its command ended.
		_, _ = io.Copy(io.Discard, pr)
		err = <-read
	}
	pr.CloseWithError(io.ErrClosedPipe)
	if err != nil {
		return err
	}
	if len(written) == 0 {
		return errNoWorld
	}
	if err := check(); err != nil {
		return err
	}
	return zw.Close()
}
