package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	avsysv1 "github.com/GT-Drehmal/avSYS/api/v1alpha1"
	"github.com/GT-Drehmal/avSYS/internal/controller"
	"github.com/GT-Drehmal/avSYS/internal/store"
)

const mojangManifest = "https://launchermeta.mojang.com/mc/game/version_manifest_v2.json"

// manifest is Mojang's list of game versions, newest first, less the ones
// no server can be made of.
type manifest struct {
	Latest   map[string]string `json:"latest"`
	Versions []gameVersion     `json:"versions"`
}

type gameVersion struct {
	ID          string `json:"id"`
	Type        string `json:"type"`
	ReleaseTime string `json:"releaseTime"`
	// Java is the Java the version ships with, where its name tells. It
	// does for a release and for a pre-release named after one, and not for
	// the snapshots that were named by the week they came out in.
	Java int `json:"java,omitempty"`
}

func (s *Server) manifest(ctx context.Context) (*manifest, error) {
	body, err := s.catalog.get(ctx, mojangManifest, time.Hour)
	if err != nil {
		return nil, err
	}
	m := &manifest{}
	if err := json.Unmarshal(body, m); err != nil {
		return nil, err
	}
	m.Versions = installable(m.Versions)
	for i := range m.Versions {
		m.Versions[i].Java = controller.GameJava(m.Versions[i].ID)
	}
	return m, nil
}

// firstServer is the oldest version Mojang publishes a server for. The list
// goes back further, to versions that only ever had a client.
const firstServer = "1.2.5"

// installable drops the versions no server can be made of: everything
// before the first one a server was published for, and the alpha and beta
// versions, which have none either. versions is newest first.
func installable(versions []gameVersion) []gameVersion {
	out := make([]gameVersion, 0, len(versions))
	for _, v := range versions {
		if v.Type != "old_alpha" && v.Type != "old_beta" {
			out = append(out, v)
		}
		if v.ID == firstServer {
			break
		}
	}
	return out
}

// builds is where each kind of server says which game versions it has a
// build for, and how to read the answer. Vanilla is not here: it has every
// version Mojang lists.
var builds = map[avsysv1.Loader]struct {
	url  string
	read func([]byte) ([]string, error)
}{
	avsysv1.LoaderPaper:    {"https://fill.papermc.io/v3/projects/paper", readPaper},
	avsysv1.LoaderFolia:    {"https://fill.papermc.io/v3/projects/folia", readPaper},
	avsysv1.LoaderPurpur:   {"https://api.purpurmc.org/v2/purpur", readPurpur},
	avsysv1.LoaderFabric:   {"https://meta.fabricmc.net/v2/versions/game", readFabric},
	avsysv1.LoaderQuilt:    {"https://meta.quiltmc.org/v3/versions/game", readFabric},
	avsysv1.LoaderForge:    {"https://files.minecraftforge.net/net/minecraftforge/forge/promotions_slim.json", readForge},
	avsysv1.LoaderNeoForge: {"https://maven.neoforged.net/api/maven/versions/releases/net/neoforged/neoforge", readNeoForge},
}

func readPaper(body []byte) ([]string, error) {
	var in struct {
		Versions map[string][]string `json:"versions"`
	}
	if err := json.Unmarshal(body, &in); err != nil {
		return nil, err
	}
	var out []string
	for _, group := range in.Versions {
		out = append(out, group...)
	}
	return out, nil
}

func readPurpur(body []byte) ([]string, error) {
	var in struct {
		Versions []string `json:"versions"`
	}
	err := json.Unmarshal(body, &in)
	return in.Versions, err
}

func readFabric(body []byte) ([]string, error) {
	var in []struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(body, &in); err != nil {
		return nil, err
	}
	out := make([]string, len(in))
	for i, v := range in {
		out[i] = v.Version
	}
	return out, nil
}

// readForge reads Forge's promotions, which are keyed "1.21.4-latest" and
// "1.21.4-recommended".
func readForge(body []byte) ([]string, error) {
	var in struct {
		Promos map[string]string `json:"promos"`
	}
	if err := json.Unmarshal(body, &in); err != nil {
		return nil, err
	}
	var out []string
	for key := range in.Promos {
		if i := strings.LastIndex(key, "-"); i > 0 {
			out = append(out, key[:i])
		}
	}
	return out, nil
}

func readNeoForge(body []byte) ([]string, error) {
	var in struct {
		Versions []string `json:"versions"`
	}
	if err := json.Unmarshal(body, &in); err != nil {
		return nil, err
	}
	var out []string
	for _, v := range in.Versions {
		if game := neoforgeGame(v); game != "" {
			out = append(out, game)
		}
	}
	return out, nil
}

// neoforgeGame is the game version a NeoForge build is for, or "" for a
// pre-release build, which the server image does not install unless asked
// for by number.
//
// Up to 1.21.11 NeoForge left off the game's leading "1.": 21.4.38 is for
// 1.21.4 and 21.0.167 for 1.21. Since then it leads with the game version
// whole: 26.1.2.46 is for 26.1.2 and 26.3.0.76 for 26.3.
func neoforgeGame(build string) string {
	if strings.Contains(build, "-") {
		return ""
	}
	parts := strings.Split(build, ".")
	for _, p := range parts {
		if _, err := strconv.Atoi(p); err != nil {
			return ""
		}
	}
	var game []string
	switch major, _ := strconv.Atoi(parts[0]); {
	case major >= 26 && len(parts) == 4:
		game = parts[:3]
	case major < 26 && len(parts) == 3:
		game = []string{"1", parts[0], parts[1]}
	default:
		return ""
	}
	if game[2] == "0" {
		game = game[:2]
	}
	return strings.Join(game, ".")
}

// supported is the game versions one kind of server can run.
type supported struct {
	// releases are the released ones, newest first.
	releases []gameVersion
	// all has pre-releases and snapshots too.
	all map[string]bool
}

// supportedBy asks which game versions a kind of server has a build for. It
// fails when that cannot be found out, which is not the same as there being
// none.
func (s *Server) supportedBy(ctx context.Context, loader avsysv1.Loader) (*supported, error) {
	m, err := s.manifest(ctx)
	if err != nil {
		return nil, err
	}
	out := &supported{all: map[string]bool{}}
	if source, ok := builds[loader]; ok {
		// Shorter than for Mojang's list: a build for a new game version is
		// what people are waiting for.
		body, err := s.catalog.get(ctx, source.url, 10*time.Minute)
		if err != nil {
			return nil, err
		}
		versions, err := source.read(body)
		if err != nil {
			return nil, err
		}
		// A loader can list a game version no server was ever published
		// for, and a build of it for one is no use: Forge has them for 1.1.
		installs := map[string]bool{}
		for _, v := range m.Versions {
			installs[v.ID] = true
		}
		for _, v := range versions {
			if installs[v] {
				out.all[v] = true
			}
		}
	} else {
		for _, v := range m.Versions {
			out.all[v.ID] = true
		}
	}
	for _, v := range m.Versions {
		if v.Type == "release" && out.all[v.ID] {
			out.releases = append(out.releases, v)
		}
	}
	return out, nil
}

// minecraftVersions lists game versions. With ?loader= it lists the ones
// that kind of server has a build for, and its "latest" is the newest of
// those rather than the newest there is.
func (s *Server) minecraftVersions(w http.ResponseWriter, r *http.Request, _ *store.User) error {
	m, err := s.manifest(r.Context())
	if err != nil {
		return err
	}
	if loader := avsysv1.Loader(r.URL.Query().Get("loader")); loader != "" {
		if _, known := loaderNames[string(loader)]; !known {
			return errBadRequest("%q is not a kind of server.", loader)
		}
		// When its own list cannot be read, every version is offered, as
		// before: a list that may hold a wrong entry beats an empty one.
		if sup, err := s.supportedBy(r.Context(), loader); err != nil {
			s.Log.Warn("listing the game versions a loader supports", "loader", loader, "error", err)
		} else {
			// Pre-releases it has a build for are listed too, so that a
			// server on one is not taken for one with no build.
			all := m.Versions
			m.Versions, m.Latest = nil, map[string]string{"release": "", "snapshot": ""}
			for _, v := range all {
				if !sup.all[v.ID] {
					continue
				}
				m.Versions = append(m.Versions, v)
				kind := "snapshot"
				if v.Type == "release" {
					kind = "release"
				}
				if m.Latest[kind] == "" {
					m.Latest[kind] = v.ID
				}
			}
		}
	}
	out, err := json.Marshal(m)
	if err != nil {
		return err
	}
	writeRawJSON(w, out, 10*time.Minute)
	return nil
}

// latestRelease is the newest released game version a kind of server has a
// build for, or "" if that cannot be found out.
func (s *Server) latestRelease(ctx context.Context, loader avsysv1.Loader) string {
	sup, err := s.supportedBy(ctx, loader)
	if err != nil || len(sup.releases) == 0 {
		return ""
	}
	return sup.releases[0].ID
}

// checkVersion refuses a game version the server's software has no build
// for: such a server would fail to install each time it started. Whatever
// cannot be checked is let through.
func (s *Server) checkVersion(ctx context.Context, spec *avsysv1.MinecraftServerSpec) error {
	// A modpack names its own game version.
	if spec.Modpack != nil || spec.Version == "" || spec.Version == "LATEST" {
		return nil
	}
	// A NeoForge build asked for by number may be one of the pre-release
	// builds, which are not counted here and can be all a game version has.
	if spec.Loader == avsysv1.LoaderNeoForge && spec.LoaderVersion != "" {
		return nil
	}
	sup, err := s.supportedBy(ctx, spec.Loader)
	if err != nil || sup.all[spec.Version] {
		return nil
	}
	// For the game itself the list is of what a server exists for at all.
	if spec.Loader == avsysv1.LoaderVanilla {
		return errBadRequest("There is no server for Minecraft %s.", spec.Version)
	}
	name := loaderNames[string(spec.Loader)]
	if len(sup.releases) == 0 {
		return errBadRequest("%s has no build for Minecraft %s.", name, spec.Version)
	}
	return errBadRequest("%s has no build for Minecraft %s. The newest it has one for is %s.", name, spec.Version, sup.releases[0].ID)
}
