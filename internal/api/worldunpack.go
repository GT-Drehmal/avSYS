package api

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/bzip2"
	"compress/gzip"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/GT-Drehmal/avSYS/internal/backup"
)

// A world uploaded to the library is kept as its own files, and so is one the
// library kept as an archive before (see worldsunpack.go): restic finds what
// such a world has in common with the snapshots of each server it is put in,
// which it cannot for an archive of it. The archive is unpacked here, on the
// panel, into a folder beside the repository, and the folder kept. The
// panel's image has no bsdtar: the archive is read in Go, by the rules a
// server's import follows in its workspace. Only files and folders come out
// of it, never links, and no session.lock; nothing goes outside the folder it
// is unpacked into; what is unpacked is held to the room there is; and the
// world is the folder the page found in it, which must have a level.dat, with
// the Nether and the End beside it where they were kept apart.
//
// It is unpacked as a server has its files, under data: the world as
// data/world, and the Nether and the End beside it as data/world_nether and
// data/world_the_end. Kept from the folder above data, a world's snapshot has
// it at /data/world, where a server's snapshot has its own.

const (
	// keptData is the folder a world is unpacked into, in its work folder:
	// /data in its snapshot.
	keptData = "data"
	// keptWorld is the world's folder there.
	keptWorld = "world"
	// maxWorldFiles is how many files and folders an archive may unpack to:
	// many times what any world has.
	maxWorldFiles = 1 << 21
)

// unpackedWorld is a world unpacked into a work folder.
type unpackedWorld struct {
	// Folders are the world's folders there, data/world and those of its
	// Nether and End beside it, as they are named in it.
	Folders []string
	// Files and Bytes are how many files it has and how large they are.
	Files, Bytes int64
}

// worldEntry is where an entry of an archive goes in the folder it is
// unpacked into, if it is of the world: the folder root in it ("" for its
// top) as data/world, and the folders beside root named for its Nether and
// End as data/world_nether and data/world_the_end. An entry that would go
// outside the folder is not of the world.
func worldEntry(entry, root string) (string, bool) {
	for strings.HasPrefix(entry, "./") {
		entry = entry[2:]
	}
	entry = strings.TrimSuffix(entry, "/")
	if entry == "" || entry == "." || path.IsAbs(entry) {
		return "", false
	}
	for _, part := range strings.Split(entry, "/") {
		if part == ".." {
			return "", false
		}
	}
	entry = path.Clean(entry)
	world := keptData + "/" + keptWorld
	if root == "" {
		return world + "/" + entry, true
	}
	if entry == root {
		return world, true
	}
	if rest, ok := strings.CutPrefix(entry, root+"/"); ok {
		return world + "/" + rest, true
	}
	for _, suffix := range []string{"_nether", "_the_end"} {
		beside := path.Join(path.Dir(root), path.Base(root)+suffix)
		if entry == beside {
			return world + suffix, true
		}
		if rest, ok := strings.CutPrefix(entry, beside+"/"); ok {
			return world + suffix + "/" + rest, true
		}
	}
	return "", false
}

// errNoRoomFor says that a world does not fit in the room there is.
func errNoRoomFor(free int64) error {
	return &Error{Status: http.StatusInsufficientStorage, Code: "no_room", Message: fmt.Sprintf("The world is larger than the %s free on the panel.", sizeOf(max(free, 0)))}
}

// unpacker writes what is of the world in an archive into a folder.
type unpacker struct {
	dir, root string
	// room is how many bytes may be written, and used how many have been.
	room, used int64
	// free is how much room there was, for saying so.
	free    int64
	entries int
	// had is whether anything of the world was in it.
	had bool
}

func (u *unpacker) target(entry string) (string, bool, error) {
	to, ok := worldEntry(entry, u.root)
	if !ok {
		return "", false, nil
	}
	u.had = true
	if u.entries++; u.entries > maxWorldFiles {
		return "", false, errBadRequest("The archive has more files than any world has.")
	}
	return filepath.Join(u.dir, filepath.FromSlash(to)), true, nil
}

func (u *unpacker) folder(entry string) error {
	to, ok, err := u.target(entry)
	if err != nil || !ok {
		return err
	}
	if err := os.MkdirAll(to, 0o755); err != nil {
		return errBadRequest("Could not unpack %s from the archive.", entry)
	}
	return nil
}

func (u *unpacker) file(entry string, r io.Reader, modified time.Time) error {
	if path.Base(strings.TrimSuffix(entry, "/")) == "session.lock" {
		return nil
	}
	to, ok, err := u.target(entry)
	if err != nil || !ok {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(to), 0o755); err != nil {
		return errBadRequest("Could not unpack %s from the archive.", entry)
	}
	out, err := os.OpenFile(to, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return errBadRequest("Could not unpack %s from the archive.", entry)
	}
	n, err := io.Copy(out, io.LimitReader(r, u.room-u.used+1))
	u.used += n
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if u.used > u.room {
		return errNoRoomFor(u.free)
	}
	if err != nil {
		var pathErr *fs.PathError
		if errors.As(err, &pathErr) {
			return err
		}
		return errBadRequest("Could not unpack %s from the archive: %v.", entry, err)
	}
	if !modified.IsZero() {
		_ = os.Chtimes(to, modified, modified)
	}
	return nil
}

func (u *unpacker) zip(zr *zip.Reader) error {
	// The room it needs is said in it, and checked before anything is
	// written; as each file is written, what it holds is checked against
	// what it said.
	var need uint64
	for _, f := range zr.File {
		if _, ok := worldEntry(f.Name, u.root); ok {
			need += f.UncompressedSize64
		}
	}
	if need > uint64(u.room) {
		return errNoRoomFor(u.free)
	}
	for _, f := range zr.File {
		mode := f.Mode()
		switch {
		case mode&fs.ModeSymlink != 0:
			continue
		case mode.IsDir() || strings.HasSuffix(f.Name, "/"):
			if err := u.folder(f.Name); err != nil {
				return err
			}
		case mode.IsRegular():
			if _, ok := worldEntry(f.Name, u.root); !ok {
				continue
			}
			r, err := f.Open()
			if err != nil {
				return errBadRequest("Could not unpack %s from the archive: %v.", f.Name, err)
			}
			err = u.file(f.Name, r, f.Modified)
			r.Close()
			if err != nil {
				return err
			}
		}
	}
	return nil
}

func (u *unpacker) tar(tr *tar.Reader) error {
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return nil
		} else if err != nil {
			return errBadRequest("Could not unpack the archive: %v.", err)
		}
		switch h.Typeflag {
		case tar.TypeDir:
			if err := u.folder(h.Name); err != nil {
				return err
			}
		case tar.TypeReg, tar.TypeRegA:
			if err := u.file(h.Name, tr, h.ModTime); err != nil {
				return err
			}
		}
		// Links, devices and the rest are left out.
	}
}

// unpackWorld unpacks the world in the archive at file, a zip or a tar,
// gzipped or not, whose folder is root ("" for its top), into dir: at most
// room bytes of it, of the free there was.
func unpackWorld(file, root, dir string, room, free int64) (*unpackedWorld, error) {
	f, err := os.Open(file)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	head := make([]byte, 512)
	n, _ := io.ReadFull(f, head)
	head = head[:n]
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	u := &unpacker{dir: dir, root: root, room: room, free: free}
	switch {
	case bytes.HasPrefix(head, []byte("PK")):
		fi, err := f.Stat()
		if err != nil {
			return nil, err
		}
		// Where its paths would lead is worldEntry's to say.
		zr, err := zip.NewReader(f, fi.Size())
		if err != nil && !(errors.Is(err, zip.ErrInsecurePath) && zr != nil) {
			return nil, errBadRequest("This zip file is damaged or cut short.")
		}
		err = u.zip(zr)
		if err != nil {
			return nil, err
		}
	case bytes.HasPrefix(head, []byte{0x1f, 0x8b}):
		gz, err := gzip.NewReader(f)
		if err != nil {
			return nil, errBadRequest("This archive is damaged or cut short.")
		}
		if err := u.tar(tar.NewReader(gz)); err != nil {
			return nil, err
		}
	case bytes.HasPrefix(head, []byte("BZh")):
		if err := u.tar(tar.NewReader(bzip2.NewReader(f))); err != nil {
			return nil, err
		}
	case len(head) >= 262 && string(head[257:262]) == "ustar":
		if err := u.tar(tar.NewReader(f)); err != nil {
			return nil, err
		}
	default:
		return nil, errBadRequest("Use a .zip or .tar.gz file.")
	}
	where := "The archive"
	if root != "" {
		where = root
	}
	if !u.had {
		if root == "" {
			return nil, errBadRequest("The archive is empty.")
		}
		return nil, errBadRequest("The archive has no folder %s.", root)
	}
	world := filepath.Join(dir, keptData, keptWorld)
	if fi, err := os.Stat(filepath.Join(world, "level.dat")); err != nil || !fi.Mode().IsRegular() {
		return nil, errBadRequest("%s has no level.dat, so it is not a world.", where)
	}
	out := &unpackedWorld{}
	for _, suffix := range []string{"", "_nether", "_the_end"} {
		folder := keptData + "/" + keptWorld + suffix
		if fi, err := os.Stat(filepath.Join(dir, filepath.FromSlash(folder))); err == nil && fi.IsDir() {
			out.Folders = append(out.Folders, folder)
		}
	}
	// What is there now, which is what is kept: a file the archive had
	// twice is there once.
	err = filepath.WalkDir(filepath.Join(dir, keptData), func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type().IsRegular() {
			fi, err := d.Info()
			if err != nil {
				return err
			}
			out.Files++
			out.Bytes += fi.Size()
		}
		return nil
	})
	return out, err
}

// worldWork is a folder of its own to unpack a world in, which done
// removes.
func (s *Server) worldWork(org string) (dir string, done func(), err error) {
	name := make([]byte, 8)
	if _, err := rand.Read(name); err != nil {
		return "", nil, err
	}
	dir = filepath.Join(s.Backups.WorkDir(org), hex.EncodeToString(name))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", nil, err
	}
	return dir, func() {
		if err := os.RemoveAll(dir); err != nil {
			s.Log.Warn("could not remove a world unpacked to be kept", "dir", dir, "error", err)
		}
	}, nil
}

// worldRoomReserve is room kept free while a world is unpacked, besides
// what restic is to add to the repository: for everything else the panel
// writes there meanwhile.
const worldRoomReserve = 256 << 20

// keepUnpacked unpacks the world in the archive at file, its folder root in
// it, into dir, a work folder of its own, and keeps it as its own files in
// a snapshot under host, as by's. drop, if given, is called once the archive
// is unpacked, for it to go before the world is kept: the room it took is
// free again. What is kept is checked against what was unpacked: the same
// files, as large, and a level.dat.
func (s *Server) keepUnpacked(ctx context.Context, org, dir, file, root, host, by string, drop func()) (*backup.Snapshot, *unpackedWorld, error) {
	free, err := freeSpace(dir)
	if err != nil {
		return nil, nil, err
	}
	fi, err := os.Stat(file)
	if err != nil {
		return nil, nil, err
	}
	// What restic adds to the repository is at most about the world's own
	// size compressed, which the archive is.
	room := free - fi.Size() - worldRoomReserve
	unpacked, err := unpackWorld(file, root, dir, room, free)
	if err != nil {
		return nil, nil, err
	}
	if drop != nil {
		drop()
	}
	kept, err := s.Backups.KeepFolders(ctx, org, dir, unpacked.Folders, host, by)
	if err != nil {
		return nil, nil, err
	}
	if err := s.checkKept(ctx, org, kept, unpacked); err != nil {
		if derr := s.Backups.Delete(context.WithoutCancel(ctx), org, kept.ID); derr != nil {
			s.Log.Error("could not remove a world kept wrong", "snapshot", kept.ID, "error", derr)
		}
		return nil, nil, err
	}
	return kept, unpacked, nil
}

// checkKept says whether a world kept as its own files is the one unpacked:
// as many files, as large, and its level.dat there.
func (s *Server) checkKept(ctx context.Context, org string, kept *backup.Snapshot, unpacked *unpackedWorld) error {
	if kept.Files != unpacked.Files || kept.Size != unpacked.Bytes {
		return fmt.Errorf("kept %d files of %d bytes of the %d files of %d bytes unpacked", kept.Files, kept.Size, unpacked.Files, unpacked.Bytes)
	}
	level, err := s.Backups.File(ctx, org, kept.ID, "/"+keptData+"/"+keptWorld+"/level.dat", 64<<20)
	if err != nil {
		return err
	}
	if level == nil {
		return errors.New("the world was kept without its level.dat")
	}
	return nil
}
