package api

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"io"
	"path"
	"regexp"
	"slices"
	"strings"

	"sigs.k8s.io/yaml"
)

// The loader goes by mod IDs, not by file names or where a file came from:
// a fork built from another mod's source keeps its ID, a renamed file is the
// same mod, and a mod can carry others inside it (jar-in-jar), which the
// loader loads as if they were files of their own. So whether a server has
// what other content needs is told by these.

const (
	// maxNestedDepth bounds how deep jars inside jars are read, and
	// maxNestedJar how large one read whole into memory may be.
	maxNestedDepth = 3
	maxNestedJar   = 64 << 20
	maxMetaFile    = 1 << 20
)

// modIDs are the mod IDs a jar gives a server: from fabric.mod.json and
// quilt.mod.json (id and provides), mods.toml and neoforge.mods.toml
// (every modId), plugin.yml and paper-plugin.yml (name and provides), and
// the same of the jars inside it that the loader loads too (fabric's and
// quilt's "jars", Forge's META-INF/jarjar/metadata.json).
func modIDs(zr *zip.Reader) []string {
	var out []string
	modIDsAt(zr, 0, &out)
	return out
}

func modIDsAt(zr *zip.Reader, depth int, out *[]string) {
	read := func(name string) ([]byte, error) {
		f, err := zr.Open(name)
		if err != nil {
			return nil, err
		}
		defer f.Close()
		raw, err := io.ReadAll(io.LimitReader(f, maxMetaFile))
		return bytes.TrimPrefix(raw, []byte("\xef\xbb\xbf")), err
	}
	var nested []string
	if raw, err := read("fabric.mod.json"); err == nil {
		var f struct {
			ID       string   `json:"id"`
			Provides []string `json:"provides"`
			Jars     []struct {
				File string `json:"file"`
			} `json:"jars"`
		}
		// Some say "provides" in a way the loader would not read: what can
		// be read is.
		_ = json.Unmarshal(raw, &f)
		addID(out, f.ID)
		for _, p := range f.Provides {
			addID(out, p)
		}
		for _, j := range f.Jars {
			nested = append(nested, j.File)
		}
	} else if raw, err := read("quilt.mod.json"); err == nil {
		var q struct {
			Loader struct {
				ID       string            `json:"id"`
				Provides []json.RawMessage `json:"provides"`
				Jars     []string          `json:"jars"`
			} `json:"quilt_loader"`
		}
		_ = json.Unmarshal(raw, &q)
		addID(out, q.Loader.ID)
		for _, p := range q.Loader.Provides {
			var id string
			if json.Unmarshal(p, &id) != nil {
				var o struct {
					ID string `json:"id"`
				}
				_ = json.Unmarshal(p, &o)
				id = o.ID
			}
			// Quilt may write "group:id"; the loader knows it by the ID.
			if _, after, ok := strings.Cut(id, ":"); ok {
				id = after
			}
			addID(out, id)
		}
		nested = q.Loader.Jars
	} else if raw, err := firstOf(read, "META-INF/neoforge.mods.toml", "META-INF/mods.toml"); err == nil {
		for _, id := range tomlModIDs(raw) {
			addID(out, id)
		}
		if raw, err := read("META-INF/jarjar/metadata.json"); err == nil {
			var jj struct {
				Jars []struct {
					Path string `json:"path"`
				} `json:"jars"`
			}
			_ = json.Unmarshal(raw, &jj)
			for _, j := range jj.Jars {
				nested = append(nested, j.Path)
			}
		}
	} else if raw, err := firstOf(read, "paper-plugin.yml", "plugin.yml"); err == nil {
		var p struct {
			Name     any `json:"name"`
			Provides any `json:"provides"`
		}
		if yaml.Unmarshal(raw, &p) == nil {
			addID(out, scalar(p.Name))
			switch v := p.Provides.(type) {
			case string:
				addID(out, v)
			case []any:
				for _, x := range v {
					addID(out, scalar(x))
				}
			}
		}
	}
	if depth >= maxNestedDepth {
		return
	}
	for _, name := range nested {
		f, err := zr.Open(strings.TrimPrefix(path.Clean("/"+name), "/"))
		if err != nil {
			continue
		}
		raw, err := io.ReadAll(io.LimitReader(f, maxNestedJar+1))
		f.Close()
		if err != nil || len(raw) > maxNestedJar {
			continue
		}
		if inner, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw))); err == nil {
			modIDsAt(inner, depth+1, out)
		}
	}
}

var modIDPattern = regexp.MustCompile(`^[A-Za-z0-9_.\- ]{1,128}$`)

// addID adds a mod ID once, if it is one.
func addID(ids *[]string, id string) {
	id = strings.TrimSpace(id)
	if id != "" && modIDPattern.MatchString(id) && !slices.Contains(*ids, id) {
		*ids = append(*ids, id)
	}
}

var tomlModID = regexp.MustCompile(`^\s*modId\s*=\s*(?:"([^"]*)"|'([^']*)')`)

// tomlModIDs are the modIds of a mods.toml's [[mods]] tables; those of its
// [[dependencies.x]] tables are what it needs, not what it is.
func tomlModIDs(raw []byte) []string {
	var ids []string
	inMods := false
	for _, line := range strings.Split(string(raw), "\n") {
		if t := strings.TrimSpace(line); strings.HasPrefix(t, "[") {
			inMods = t == "[[mods]]"
			continue
		}
		if m := tomlModID.FindStringSubmatch(line); inMods && m != nil {
			ids = append(ids, m[1]+m[2])
		}
	}
	return ids
}
