package api

import (
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"path"
	"strings"

	"github.com/pkg/sftp"
)

// The workspace's file server says little of why something failed: most
// of what goes wrong (a name taken, a folder not empty, a full disk) comes
// back as a bare "Failure". What people are shown is said here instead, in
// plain words, from what the files are like once it has failed.

// fileError is what is said of something done to a server's files that
// failed, where nothing more is known of what was being done.
func fileError(err error) error {
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return errNotFound
	case errors.Is(err, fs.ErrPermission):
		return &Error{Status: http.StatusForbidden, Code: "permission", Message: "Permission denied."}
	}
	var status *sftp.StatusError
	if errors.As(err, &status) {
		return errBadRequest("The server's files could not be changed.")
	}
	return err
}

// fileFailed is what is said of doing something (verb, as "delete") to the
// file or folder at full that failed: by its name, and why where that can be
// told. An error already said, or one of the files being out of reach, is
// returned as it is.
func fileFailed(sc *sftp.Client, err error, verb, full string) error {
	name := nameOf(full)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return &Error{Status: http.StatusNotFound, Code: "not_found", Message: fmt.Sprintf("%s was not found.", name)}
	case errors.Is(err, fs.ErrPermission):
		return &Error{Status: http.StatusForbidden, Code: "permission", Message: fmt.Sprintf("No permission to %s %s.", verb, name)}
	}
	var status *sftp.StatusError
	if !errors.As(err, &status) {
		return err
	}
	if diskFull(sc, path.Dir(full)) {
		return errDiskFull
	}
	return errBadRequest("Could not %s %s.", verb, name)
}

// nameOf is what a file or folder is called in what people are shown.
func nameOf(full string) string {
	if full == dataRoot {
		return "the server's files"
	}
	return path.Base(full)
}

var errDiskFull = &Error{Status: http.StatusInsufficientStorage, Code: "no_room", Message: "The server's disk is full."}

// diskFull is whether there is no room left where dir is. Asked once a
// write has failed, and before what it wrote is taken away again.
func diskFull(sc *sftp.Client, dir string) bool {
	vfs, err := sc.StatVFS(dir)
	if err != nil {
		return false
	}
	return int64(vfs.Bavail)*int64(vfs.Frsize) < 1<<20 || vfs.Files > 0 && vfs.Favail == 0
}

// errExists says something is already at full, and what it is.
func errExists(full string, dir bool) error {
	kind := "file"
	if dir {
		kind = "folder"
	}
	return &Error{Status: http.StatusConflict, Code: "exists", Message: fmt.Sprintf("A %s named %s already exists.", kind, nameOf(full))}
}

// errNoFolder says there is no folder at dir for something to go in.
func errNoFolder(dir string) error {
	return errBadRequest("There is no folder %s.", relPath(dir))
}

// placeable says why a file cannot be written at full: a folder of that name
// there, or no folder for it to go in. Files out of reach are returned as
// they are.
func placeable(sc *sftp.Client, full string) error {
	if fi, err := sc.Stat(full); err == nil && fi.IsDir() {
		return errExists(full, true)
	} else if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	fi, err := sc.Stat(path.Dir(full))
	if errors.Is(err, fs.ErrNotExist) || err == nil && !fi.IsDir() {
		return errNoFolder(path.Dir(full))
	}
	return err
}

// folderFree says why a folder cannot be made at full: something there by
// its name, or a file where one of the folders it is in would be.
func folderFree(sc *sftp.Client, full string) error {
	at := dataRoot
	for _, part := range strings.Split(strings.TrimPrefix(full, dataRoot+"/"), "/") {
		at = path.Join(at, part)
		fi, err := sc.Stat(at)
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		} else if err != nil {
			return err
		}
		if at == full {
			return errExists(full, fi.IsDir())
		}
		if !fi.IsDir() {
			return &Error{Status: http.StatusConflict, Code: "exists", Message: fmt.Sprintf("%s is a file, not a folder.", nameOf(at))}
		}
	}
	return nil
}

// removeAll deletes the file or folder at full, and all that is in it. A
// link is removed itself, never what it points to: Client.RemoveAll follows
// one, to empty the folder it points at, and cannot remove one that points
// nowhere. Said of what could not be removed, by its name.
func removeAll(sc *sftp.Client, full string) error {
	if _, err := sc.Lstat(full); err != nil {
		return fileFailed(sc, err, "delete", full)
	}
	return removeTree(sc, full)
}

// removeTree is removeAll for what may have gone meanwhile: that is done.
func removeTree(sc *sftp.Client, full string) error {
	fi, err := sc.Lstat(full)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	} else if err != nil {
		return fileFailed(sc, err, "delete", full)
	}
	if !fi.IsDir() {
		if err := sc.Remove(full); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fileFailed(sc, err, "delete", full)
		}
		return nil
	}
	entries, err := sc.ReadDir(full)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fileFailed(sc, err, "delete", full)
	}
	for _, e := range entries {
		if err := removeTree(sc, path.Join(full, e.Name())); err != nil {
			return err
		}
	}
	if err := sc.RemoveDirectory(full); err != nil && !errors.Is(err, fs.ErrNotExist) {
		// Something put in it meanwhile, as a running game does.
		if left, rerr := sc.ReadDir(full); rerr == nil && len(left) > 0 {
			return &Error{Status: http.StatusConflict, Code: "changed", Message: fmt.Sprintf("Files were added to %s as it was deleted. Try again.", nameOf(full))}
		}
		return fileFailed(sc, err, "delete", full)
	}
	return nil
}

// taken says what is at full already, if anything: a link by what it
// points to.
func taken(sc *sftp.Client, full string) error {
	fi, err := sc.Lstat(full)
	if err != nil {
		return nil
	}
	if fi.Mode()&fs.ModeSymlink != 0 {
		if target, err := sc.Stat(full); err == nil {
			fi = target
		}
	}
	return errExists(full, fi.IsDir())
}

// notPlaced is what is said of a file that could not be written, or moved
// into place once it was, at full.
func notPlaced(sc *sftp.Client, err error, full string) error {
	var said *Error
	if why := placeable(sc, full); errors.As(why, &said) {
		return said
	}
	return fileFailed(sc, err, "save", full)
}
