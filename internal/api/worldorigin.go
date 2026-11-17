package api

import (
	"bufio"
	"compress/gzip"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/pkg/sftp"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	avsysv1 "github.com/GT-Drehmal/avSYS/api/v1alpha1"
	"github.com/GT-Drehmal/avSYS/internal/backup"
	"github.com/GT-Drehmal/avSYS/internal/store"
)

// A server keeps where its world came from (spec.worldOrigin), which the
// panel writes as it puts a world in: whichever way a server is made, its
// world replaced, copied or restored. The server's page shows it, beside
// what the world itself says of itself.

// newWorldOrigin is a world the game makes, with the seed the server's
// settings give it.
func newWorldOrigin(spec *avsysv1.MinecraftServerSpec) *avsysv1.WorldOrigin {
	return &avsysv1.WorldOrigin{Kind: avsysv1.WorldNew, Seed: strings.TrimSpace(spec.Properties["level-seed"]), Time: metav1.Now()}
}

// carriedOrigin is the origin of a world that came with files from
// elsewhere, by way of via: a copy of a server, a snapshot or a template.
// The world is still what it was there, so it keeps where it came from
// before, with what it came by way of now; where nothing was known of that,
// via is all there is. A copy of a server made from the library is then
// still that library world, and can be put back to it.
func carriedOrigin(was *avsysv1.WorldOrigin, via avsysv1.WorldOrigin) *avsysv1.WorldOrigin {
	via.Time = metav1.Now()
	if was == nil {
		return &via
	}
	switch was.Kind {
	case avsysv1.WorldNew, avsysv1.WorldLibrary, avsysv1.WorldUpload, avsysv1.WorldArchive:
	default:
		// Only what it came by way of was known: now that is this.
		return &via
	}
	o := was.DeepCopy()
	o.Server, o.Snapshot, o.Template, o.Time = via.Server, via.Snapshot, via.Template, via.Time
	return o
}

// importedOrigin is where the world an import puts in comes from, as its
// request says: ?world= "new" or "none" (a server's archive whose world is
// left out) is one the game makes, with the seed the new world is asked to
// be made with; ?library= and ?template= are the library's world and the
// template's own, given here; ?world=true is a world uploaded, and none a
// server's archive with its world, each named by ?file=.
func importedOrigin(q url.Values, library *store.World, template *store.Template, own *ownWorld, made map[string]*string) *avsysv1.WorldOrigin {
	o := &avsysv1.WorldOrigin{Time: metav1.Now()}
	switch world := q.Get("world"); {
	case world == "new" || world == "none":
		o.Kind = avsysv1.WorldNew
		if seed := made["level-seed"]; seed != nil {
			o.Seed = strings.TrimSpace(*seed)
		}
	case library != nil:
		o.Kind, o.Library = avsysv1.WorldLibrary, &avsysv1.WorldRef{ID: library.ID, Name: library.Name}
	case template != nil:
		var was *avsysv1.WorldOrigin
		if own != nil && own.Library != nil {
			was = &avsysv1.WorldOrigin{Kind: avsysv1.WorldLibrary, Library: own.Library.DeepCopy()}
		}
		return carriedOrigin(was, avsysv1.WorldOrigin{Kind: avsysv1.WorldTemplate, Template: &avsysv1.WorldRef{ID: template.ID, Name: template.Name}})
	case world == "true":
		o.Kind, o.File = avsysv1.WorldUpload, sentFileName(q.Get("file"))
	default:
		o.Kind, o.File = avsysv1.WorldArchive, sentFileName(q.Get("file"))
	}
	return o
}

// sentFileName is an archive's name as a request gives it: a plain name, of
// sensible length, or none.
func sentFileName(name string) string {
	name = path.Base(strings.ReplaceAll(strings.TrimSpace(name), "\\", "/"))
	if !plainName(name) || strings.ContainsAny(name, "\r\n") {
		return ""
	}
	if r := []rune(name); len(r) > 200 {
		name = string(r[:200])
	}
	return name
}

// worldFacts is what a server's world says of itself, as far as it can be
// read.
type worldFacts struct {
	// Level is the world's folder.
	Level string `json:"level"`
	// Exists is false while there is no world yet: the game makes it as
	// it first starts.
	Exists bool `json:"exists"`
	// Name, Seed and Version are as its level.dat says: what it is called,
	// is made from, and the game version it was last opened in.
	Name    string `json:"name,omitempty"`
	Seed    string `json:"seed,omitempty"`
	Version string `json:"version,omitempty"`
	// LastPlayed is when it was last saved by the game.
	LastPlayed *time.Time `json:"lastPlayed,omitempty"`
	// Size is of its folders, in bytes, where they could be counted soon
	// enough.
	Size *int64 `json:"size,omitempty"`
}

// getServerWorld says what a server's world is now, as its files have it: the
// folder, level.dat's name, seed, version and time last played, and how
// large it is. It reads the server's files, so is asked for on its own,
// apart from the server.
func (s *Server) getServerWorld(w http.ResponseWriter, r *http.Request, u *store.User) error {
	a, mc, err := s.server(r, u, store.RoleViewer)
	if err != nil {
		return err
	}
	// Files a restore, copy or import is putting in are not read.
	if restoreUnfinished(mc) || importUnfinished(mc) {
		return &Error{Status: http.StatusConflict, Code: "busy", Message: "The server's files are being put in."}
	}
	sc, err := s.workspaces.get(r.Context(), a.Namespace, mc.Name)
	if err != nil {
		return s.unreachable(mc.Name, err)
	}
	facts, err := readWorldFacts(r.Context(), sc, levelOf(&mc.Spec))
	if err != nil {
		return fileError(err)
	}
	writeJSON(w, http.StatusOK, facts)
	return nil
}

// readWorldFacts reads what a world says of itself out of a server's files.
// fallback is the world's folder as the server's settings say, for when
// server.properties does not.
func readWorldFacts(ctx context.Context, sc *sftp.Client, fallback string) (worldFacts, error) {
	level := levelInProperties(sc)
	if level == "" {
		level = fallback
	}
	if level == "" {
		level = "world"
	}
	facts := worldFacts{Level: level}
	f, err := sc.Open(path.Join(dataRoot, level, "level.dat"))
	if errors.Is(err, os.ErrNotExist) {
		return facts, nil
	} else if err != nil {
		return facts, err
	}
	facts.Exists = true
	info, rerr := readLevelDat(io.LimitReader(f, 32<<20))
	f.Close()
	// One that cannot be read says nothing, which is no failure: its size
	// is still there to count.
	if rerr == nil {
		facts.Name, facts.Seed, facts.Version, facts.LastPlayed = info.Name, info.Seed, info.Version, info.LastPlayed
	}
	if size, ok := folderSize(ctx, sc, worldFolders(level)); ok {
		facts.Size = &size
	}
	return facts, nil
}

// levelInProperties is the world's folder as server.properties names it, or
// "" where it does not, or names no plain folder.
func levelInProperties(sc *sftp.Client) string {
	f, err := sc.Open(path.Join(dataRoot, "server.properties"))
	if err != nil {
		return ""
	}
	defer f.Close()
	level := ""
	scan := bufio.NewScanner(io.LimitReader(f, 1<<20))
	for scan.Scan() {
		line := strings.TrimSpace(scan.Text())
		if v, ok := strings.CutPrefix(line, "level-name="); ok {
			level = strings.TrimSpace(v)
		}
	}
	if !plainName(level) {
		return ""
	}
	return level
}

// folderSize counts the bytes in folders and all they hold. One that takes
// too long, or holds too many folders, is not counted: false.
func folderSize(ctx context.Context, sc *sftp.Client, roots []string) (int64, bool) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var total int64
	folders := 0
	queue := make([]string, 0, len(roots))
	for _, r := range roots {
		queue = append(queue, path.Join(dataRoot, r))
	}
	for len(queue) > 0 {
		dir := queue[0]
		queue = queue[1:]
		if folders++; folders > 5000 || ctx.Err() != nil {
			return 0, false
		}
		entries, err := sc.ReadDir(dir)
		if errors.Is(err, os.ErrNotExist) {
			continue
		} else if err != nil {
			return 0, false
		}
		for _, e := range entries {
			switch {
			case e.IsDir():
				queue = append(queue, path.Join(dir, e.Name()))
			case e.Mode().IsRegular():
				total += e.Size()
			}
		}
	}
	return total, true
}

// levelInfo is what a level.dat says of its world.
type levelInfo struct {
	Name       string
	Seed       string
	Version    string
	LastPlayed *time.Time
}

// readLevelDat reads a world's level.dat: NBT, gzipped as the game writes
// it, or not.
func readLevelDat(r io.Reader) (levelInfo, error) {
	var info levelInfo
	br := bufio.NewReader(r)
	var in io.Reader = br
	if head, err := br.Peek(2); err == nil && head[0] == 0x1f && head[1] == 0x8b {
		z, err := gzip.NewReader(br)
		if err != nil {
			return info, err
		}
		defer z.Close()
		in = z
	}
	n := &nbtReader{r: bufio.NewReader(io.LimitReader(in, 64<<20))}
	if t, err := n.byte(); err != nil {
		return info, err
	} else if t != nbtCompound {
		return info, errors.New("level.dat does not start with a compound")
	}
	if _, err := n.string(); err != nil {
		return info, err
	}
	root, err := n.payload(nbtCompound, 0)
	if err != nil {
		return info, err
	}
	data, _ := root.(map[string]any)["Data"].(map[string]any)
	if data == nil {
		return info, errors.New("level.dat has no Data")
	}
	info.Name, _ = data["LevelName"].(string)
	if v, ok := data["Version"].(map[string]any); ok {
		info.Version, _ = v["Name"].(string)
	}
	if gen, ok := data["WorldGenSettings"].(map[string]any); ok {
		if seed, ok := gen["seed"].(int64); ok {
			info.Seed = strconv.FormatInt(seed, 10)
		}
	}
	if info.Seed == "" {
		if seed, ok := data["RandomSeed"].(int64); ok {
			info.Seed = strconv.FormatInt(seed, 10)
		}
	}
	if ms, ok := data["LastPlayed"].(int64); ok && ms > 0 {
		t := time.UnixMilli(ms).UTC()
		info.LastPlayed = &t
	}
	return info, nil
}

const (
	nbtEnd byte = iota
	nbtByte
	nbtShort
	nbtInt
	nbtLong
	nbtFloat
	nbtDouble
	nbtByteArray
	nbtString
	nbtList
	nbtCompound
	nbtIntArray
	nbtLongArray
)

// nbtWidths are the sizes of the values of fixed size, which a list of them
// is read past as a whole.
var nbtWidths = map[byte]int64{nbtByte: 1, nbtShort: 2, nbtInt: 4, nbtFloat: 4, nbtLong: 8, nbtDouble: 8}

// nbtReader reads the game's NBT. Of the values, only strings, longs and
// compounds are kept, which is all a level.dat is read for; the rest are
// read past.
type nbtReader struct{ r *bufio.Reader }

func (n *nbtReader) byte() (byte, error) { return n.r.ReadByte() }

func (n *nbtReader) skip(count int64) error {
	_, err := io.CopyN(io.Discard, n.r, count)
	return err
}

func (n *nbtReader) int(size int) (int64, error) {
	var b [8]byte
	if _, err := io.ReadFull(n.r, b[:size]); err != nil {
		return 0, err
	}
	switch size {
	case 2:
		return int64(int16(binary.BigEndian.Uint16(b[:2]))), nil
	case 4:
		return int64(int32(binary.BigEndian.Uint32(b[:4]))), nil
	default:
		return int64(binary.BigEndian.Uint64(b[:8])), nil
	}
}

func (n *nbtReader) string() (string, error) {
	size, err := n.int(2)
	if err != nil {
		return "", err
	}
	b := make([]byte, uint16(size))
	if _, err := io.ReadFull(n.r, b); err != nil {
		return "", err
	}
	return string(b), nil
}

func (n *nbtReader) length() (int64, error) {
	size, err := n.int(4)
	if err != nil {
		return 0, err
	}
	if size < 0 {
		return 0, errors.New("negative length")
	}
	return size, nil
}

func (n *nbtReader) payload(t byte, depth int) (any, error) {
	if depth > 512 {
		return nil, errors.New("nested too deep")
	}
	switch t {
	case nbtEnd:
		return nil, nil
	case nbtByte:
		return nil, n.skip(1)
	case nbtShort:
		return nil, n.skip(2)
	case nbtInt, nbtFloat:
		return nil, n.skip(4)
	case nbtLong:
		return n.int(8)
	case nbtDouble:
		return nil, n.skip(8)
	case nbtByteArray, nbtIntArray, nbtLongArray:
		size, err := n.length()
		if err != nil {
			return nil, err
		}
		width := map[byte]int64{nbtByteArray: 1, nbtIntArray: 4, nbtLongArray: 8}[t]
		return nil, n.skip(size * width)
	case nbtString:
		return n.string()
	case nbtList:
		of, err := n.byte()
		if err != nil {
			return nil, err
		}
		size, err := n.length()
		if err != nil {
			return nil, err
		}
		// Every element reads at least a byte, so that a list cannot go
		// on longer than the file does: one of numbers is read past as a
		// whole, and one of nothing has nothing in it.
		if width, fixed := nbtWidths[of]; fixed {
			return nil, n.skip(size * width)
		}
		if of == nbtEnd {
			if size > 0 {
				return nil, errors.New("a list of nothing with something in it")
			}
			return nil, nil
		}
		for range size {
			if _, err := n.payload(of, depth+1); err != nil {
				return nil, err
			}
		}
		return nil, nil
	case nbtCompound:
		out := map[string]any{}
		for {
			tag, err := n.byte()
			if err != nil {
				return nil, err
			}
			if tag == nbtEnd {
				return out, nil
			}
			name, err := n.string()
			if err != nil {
				return nil, err
			}
			v, err := n.payload(tag, depth+1)
			if err != nil {
				return nil, err
			}
			if v != nil {
				out[name] = v
			}
		}
	default:
		return nil, fmt.Errorf("unknown tag %d", t)
	}
}

// snapshotWorldOrigin is the origin of the world of a server made from a
// snapshot, named name: a new one; the one the snapshot's server had, for a
// server brought back under its own name; or the snapshot's world, carried
// by way of the snapshot or the template it is a version of.
func snapshotWorldOrigin(spec *avsysv1.MinecraftServerSpec, recorded *avsysv1.WorldOrigin, snap *backup.Snapshot, name string, template *avsysv1.WorldRef, newWorld bool) *avsysv1.WorldOrigin {
	switch {
	case newWorld:
		return newWorldOrigin(spec)
	case template != nil:
		return carriedOrigin(recorded, avsysv1.WorldOrigin{Kind: avsysv1.WorldTemplate, Template: template})
	case snap.Server == name:
		return recorded.DeepCopy()
	default:
		return carriedOrigin(recorded, avsysv1.WorldOrigin{Kind: avsysv1.WorldSnapshot, Server: snap.Server, Snapshot: snap.ID})
	}
}
