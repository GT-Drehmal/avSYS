package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/pkg/sftp"
	"sigs.k8s.io/yaml"

	avsysv1 "github.com/GT-Drehmal/avSYS/api/v1alpha1"
	"github.com/GT-Drehmal/avSYS/internal/store"
)

// Bedrock Edition players join a Java server through Geyser, and Floodgate
// lets them in without a Java account. Floodgate gives each the UUID made
// from their XUID, the number of their Xbox account, and a name made from
// their gamertag behind a prefix. A saved list keeps a Bedrock player by
// that UUID and their gamertag, looked up with GeyserMC, which keeps the
// XUID of every gamertag a server with Geyser has seen.
//
// The game cannot look up a Bedrock player's name: "whitelist add .Name"
// says "That player does not exist" until they have joined once. Floodgate's
// own "fwhitelist add" looks the gamertag up again from the game, and
// answers nothing over RCON, as it does so after the command has returned.
// So the panel puts Bedrock players on a whitelist by its file, with the
// UUID it knows, and has the game read it again ("whitelist reload"). This
// works with Floodgate or without it, on every kind of server.

// geyserAPI is GeyserMC's global API.
const geyserAPI = "https://api.geysermc.org/v2/"

// gamertagPattern is what can be an Xbox gamertag, as it is typed: letters,
// digits and spaces, up to 16 as GeyserMC takes them. An underscore is let
// through as well, for a space Floodgate wrote as one.
var gamertagPattern = regexp.MustCompile(`^[A-Za-z0-9_](?:[A-Za-z0-9 _]{0,14}[A-Za-z0-9_])?$`)

// floodgateUUID is the UUID Floodgate gives the Bedrock player of an XUID,
// without dashes: zero, but for the XUID in the low 64 bits.
func floodgateUUID(xuid string) (string, bool) {
	n, err := strconv.ParseUint(xuid, 10, 64)
	if err != nil || n == 0 {
		return "", false
	}
	return fmt.Sprintf("%016x%016x", 0, n), true
}

// xuidOf is the XUID of a Floodgate UUID, with dashes or without, or "" for
// a UUID that is not one.
func xuidOf(uuid string) string {
	bare := bareUUID(uuid)
	if bare == "" || bare[:16] != strings.Repeat("0", 16) {
		return ""
	}
	n, err := strconv.ParseUint(bare[16:], 16, 64)
	if err != nil || n == 0 {
		return ""
	}
	return strconv.FormatUint(n, 10)
}

// dashedUUID is a UUID as the game writes it, with dashes.
func dashedUUID(uuid string) string {
	bare := bareUUID(uuid)
	if bare == "" {
		return uuid
	}
	return bare[:8] + "-" + bare[8:12] + "-" + bare[12:16] + "-" + bare[16:20] + "-" + bare[20:]
}

// bedrockListed is whether a player on a server's list is one of Bedrock:
// by the UUID Floodgate gave them, or, where the UUID is not known, by a
// name no Java account could have, as Floodgate's prefix makes it.
func bedrockListed(p listedPlayer) bool {
	if p.UUID != "" {
		return xuidOf(p.UUID) != ""
	}
	return !minecraftNamePattern.MatchString(p.Name)
}

// floodgate is how a server's Floodgate names Bedrock players.
type floodgate struct {
	prefix string
}

// defaultFloodgate is Floodgate as it comes, and as a server without it
// would name Bedrock players once it is added.
var defaultFloodgate = floodgate{prefix: "."}

// name is a Bedrock player's name in game, as Floodgate makes it from their
// gamertag: behind the prefix, cut to 16 characters in all, a space as
// "_". Floodgate can be set to keep spaces ("replace-spaces: false"); the
// panel writes "_" all the same, as a name with a space could not go in a
// command nor be read back. The game knows a whitelisted player by UUID,
// so they are let in either way.
func (f floodgate) name(gamertag string) string {
	n := min(len(gamertag), max(16-len(f.prefix), 0))
	return strings.ReplaceAll(f.prefix+gamertag[:n], " ", "_")
}

// inGame is a saved list's player's name in game.
func (f floodgate) inGame(p store.ListPlayer) string {
	if p.Bedrock() {
		return f.name(p.Name)
	}
	return p.Name
}

// gamertag is the gamertag in a Bedrock player's name in game, as far as
// it can be told: a space written as "_" stays one.
func (f floodgate) gamertag(name string) string {
	if f.prefix != "" {
		if rest, ok := strings.CutPrefix(name, f.prefix); ok {
			return rest
		}
	}
	return strings.TrimLeft(name, ".*!~+-")
}

// floodgateConfigs are where Floodgate keeps its settings: as a plugin, and
// as a Fabric or NeoForge mod.
var floodgateConfigs = []string{"plugins/floodgate/config.yml", "config/floodgate/config.yml"}

// floodgateOf is how a server's Floodgate names Bedrock players, from its
// settings where they can be read, or as it does by default.
func (s *Server) floodgateOf(ctx context.Context, mc *avsysv1.MinecraftServer) floodgate {
	sc, err := s.workspaces.getNow(ctx, mc.Namespace, mc.Name)
	if err != nil {
		return defaultFloodgate
	}
	return floodgateFrom(sc)
}

func floodgateFrom(sc *sftp.Client) floodgate {
	for _, file := range floodgateConfigs {
		f, err := sc.Open(path.Join(dataRoot, file))
		if err != nil {
			continue
		}
		data, err := io.ReadAll(io.LimitReader(f, 1<<20))
		f.Close()
		if err != nil {
			continue
		}
		var c struct {
			Prefix *string `json:"username-prefix"`
		}
		if yaml.Unmarshal(data, &c) != nil {
			continue
		}
		out := defaultFloodgate
		if c.Prefix != nil {
			out.prefix = *c.Prefix
		}
		return out
	}
	return defaultFloodgate
}

// unknownToGeyser is GeyserMC saying it does not know a gamertag or XUID:
// one it has no XUID for, and could not ask Xbox about, answers 503 so.
func unknownToGeyser(status int, body []byte) bool {
	return status == http.StatusServiceUnavailable && strings.Contains(strings.ToLower(string(body)), "cache")
}

// bedrockAccount asks GeyserMC for the Bedrock player with a gamertag. The
// gamertag is as their account has it, where GeyserMC says.
func (s *Server) bedrockAccount(ctx context.Context, gamertag string) (*store.ListPlayer, error) {
	notFound := &Error{Status: http.StatusNotFound, Code: "no_such_player", Message: "No Bedrock player found named " + gamertag + "."}
	// Asked in one case, as Xbox takes it, so that a name typed in another
	// is not asked again.
	body, err := s.catalog.getOr(ctx, geyserAPI+"xbox/xuid/"+url.PathEscape(strings.ToLower(gamertag)), 5*time.Minute, unknownToGeyser)
	if errors.Is(err, errNotFound) {
		return nil, notFound
	}
	if err != nil {
		return nil, err
	}
	var found struct {
		XUID json.Number `json:"xuid"`
	}
	if err := json.Unmarshal(body, &found); err != nil {
		return nil, &Error{Status: http.StatusBadGateway, Code: "upstream", Message: "Could not check that gamertag with GeyserMC."}
	}
	if found.XUID == "" {
		return nil, notFound
	}
	uuid, ok := floodgateUUID(found.XUID.String())
	if !ok {
		return nil, &Error{Status: http.StatusBadGateway, Code: "upstream", Message: "Could not check that gamertag with GeyserMC."}
	}
	p := &store.ListPlayer{UUID: uuid, Name: gamertag, Kind: store.KindBedrock, XUID: found.XUID.String()}
	if now, err := s.gamertagOf(ctx, p.XUID); err == nil {
		p.Name = now
	}
	return p, nil
}

// gamertagOf is the gamertag of an XUID now, as GeyserMC has it.
func (s *Server) gamertagOf(ctx context.Context, xuid string) (string, error) {
	body, err := s.catalog.getOr(ctx, geyserAPI+"xbox/gamertag/"+url.PathEscape(xuid), 6*time.Hour, unknownToGeyser)
	if err != nil {
		return "", err
	}
	return gamertagIn(body)
}

func gamertagIn(body []byte) (string, error) {
	var found struct{ Gamertag string }
	if err := json.Unmarshal(body, &found); err != nil || !gamertagPattern.MatchString(found.Gamertag) {
		return "", errNotFound
	}
	return found.Gamertag, nil
}

// currentGamertag asks GeyserMC what an XUID's gamertag is now, not
// through the catalog's cache, as currentName asks Mojang.
func (s *Server) currentGamertag(ctx context.Context, xuid string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, geyserAPI+"xbox/gamertag/"+url.PathEscape(xuid), nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", s.catalog.userAgent)
	resp, err := s.catalog.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", errNotFound
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if err != nil {
		return "", err
	}
	return gamertagIn(body)
}

// lookUpBedrock asks GeyserMC who each gamertag belongs to, a few at a
// time, as lookUpPlayers asks Mojang. A gamertag may be given as the game
// shows it, behind Floodgate's prefix. Gamertags GeyserMC does not know,
// and what could not be one, are missing; any other failure fails the
// whole.
func (s *Server) lookUpBedrock(ctx context.Context, gamertags []string) ([]store.ListPlayer, []string, error) {
	var asked, missing []string
	for _, tag := range gamertags {
		tag = strings.TrimSpace(tag)
		bare := strings.TrimSpace(strings.TrimLeft(tag, ".*!~+-"))
		switch {
		case tag == "" || hasName(asked, bare) || hasName(missing, tag):
		case !gamertagPattern.MatchString(bare):
			missing = append(missing, tag)
		default:
			asked = append(asked, bare)
		}
	}
	if len(asked) > store.MaxListPlayers {
		return nil, nil, errBadRequest("Up to %d players can be added at once.", store.MaxListPlayers)
	}
	found := make([]*store.ListPlayer, len(asked))
	errs := make([]error, len(asked))
	var wg sync.WaitGroup
	next := make(chan int)
	for range min(6, len(asked)) {
		wg.Go(func() {
			for i := range next {
				found[i], errs[i] = s.bedrockAccount(ctx, asked[i])
			}
		})
	}
	for i := range asked {
		next <- i
	}
	close(next)
	wg.Wait()
	var out []store.ListPlayer
	for i, tag := range asked {
		var e *Error
		switch {
		case errs[i] == nil:
			if !slices.ContainsFunc(out, func(p store.ListPlayer) bool { return p.UUID == found[i].UUID }) {
				out = append(out, *found[i])
			}
		case errors.As(errs[i], &e) && e.Code == "no_such_player":
			missing = append(missing, tag)
		default:
			s.Log.Warn("could not look up a Bedrock player", "gamertag", tag, "error", errs[i])
			return nil, nil, &Error{Status: http.StatusBadGateway, Code: "upstream", Message: "Could not check the gamertags with GeyserMC. Try again in a moment."}
		}
	}
	return out, missing, nil
}

// changeWhitelistFile puts players on whitelist.json and takes others off
// it, by UUID where one is given and by name otherwise, leaving every other
// entry as it was. One on it already by UUID is given the name asked for.
func changeWhitelistFile(sc *sftp.Client, file string, add, remove []listedPlayer) error {
	var entries []map[string]any
	f, err := sc.Open(file)
	switch {
	case errors.Is(err, os.ErrNotExist):
	case err != nil:
		return err
	default:
		data, err := io.ReadAll(io.LimitReader(f, 4<<20))
		f.Close()
		if err != nil {
			return err
		}
		if len(strings.TrimSpace(string(data))) > 0 {
			if err := json.Unmarshal(data, &entries); err != nil {
				return errBadRequest("%s could not be read as a list of players.", path.Base(file))
			}
		}
	}
	field := func(e map[string]any, key string) string {
		v, _ := e[key].(string)
		return v
	}
	changed := false
	for _, p := range remove {
		before := len(entries)
		entries = slices.DeleteFunc(entries, func(e map[string]any) bool {
			if p.UUID != "" {
				return bareUUID(field(e, "uuid")) == bareUUID(p.UUID)
			}
			return strings.EqualFold(field(e, "name"), p.Name)
		})
		changed = changed || len(entries) != before
	}
	for _, p := range add {
		// By UUID alone: two players can have one name in game.
		i := slices.IndexFunc(entries, func(e map[string]any) bool { return bareUUID(field(e, "uuid")) == bareUUID(p.UUID) })
		if i < 0 {
			entries = append(entries, map[string]any{"uuid": p.UUID, "name": p.Name})
			changed = true
			continue
		}
		if field(entries[i], "uuid") != p.UUID || field(entries[i], "name") != p.Name {
			entries[i]["uuid"], entries[i]["name"] = p.UUID, p.Name
			changed = true
		}
	}
	if !changed {
		return nil
	}
	if entries == nil {
		entries = []map[string]any{}
	}
	return writeListFile(sc, file, entries)
}

// changeBedrockWhitelist puts Bedrock players on a server's whitelist and
// takes them off it by its file, and has the game read it again if it runs.
func (s *Server) changeBedrockWhitelist(ctx context.Context, mc *avsysv1.MinecraftServer, add, remove []listedPlayer) error {
	sc, err := s.workspaces.getNow(ctx, mc.Namespace, mc.Name)
	if err != nil {
		return err
	}
	if err := changeWhitelistFile(sc, path.Join(dataRoot, "whitelist.json"), add, remove); err != nil {
		return err
	}
	if mc.Status.Phase != avsysv1.PhaseRunning {
		return nil
	}
	_, err = s.listCommand(ctx, mc, "whitelist reload")
	return err
}
