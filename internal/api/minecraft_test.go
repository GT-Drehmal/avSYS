package api

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"sigs.k8s.io/controller-runtime/pkg/client"

	avsysv1 "github.com/GT-Drehmal/avSYS/api/v1alpha1"
	"github.com/GT-Drehmal/avSYS/internal/store"
)

// Who is an operator in game for their role: the linked players of members
// at or above the server's role.
func TestWantedOps(t *testing.T) {
	members := []store.Member{
		{Username: "owner", Role: store.RoleOwner, MinecraftName: "Olive"},
		{Username: "admin", Role: store.RoleAdmin, MinecraftName: "Ada"},
		{Username: "operator", Role: store.RoleOperator, MinecraftName: "Otto"},
		{Username: "viewer", Role: store.RoleViewer, MinecraftName: "Vera"},
		{Username: "unlinked", Role: store.RoleOwner},
		// Two members who gave the same player are one operator.
		{Username: "twin", Role: store.RoleAdmin, MinecraftName: "ada"},
	}
	for setting, want := range map[string]string{
		"":         "Ada Olive Otto",
		"operator": "Ada Olive Otto",
		"admin":    "Ada Olive",
		"owner":    "Olive",
		"off":      "",
	} {
		got := wantedOps(&avsysv1.MinecraftServerSpec{AutoOp: setting}, members)
		if strings.Join(got, " ") != want {
			t.Errorf("autoOp %q: %q, want %q", setting, strings.Join(got, " "), want)
		}
	}
	// A viewer is never one, whatever they linked.
	if got := wantedOps(&avsysv1.MinecraftServerSpec{}, members[3:4]); len(got) != 0 {
		t.Errorf("a viewer was made an operator: %v", got)
	}
}

// Only operators the panel made are taken off by it, and not ones that are
// also set by hand.
func TestOpChanges(t *testing.T) {
	for name, c := range map[string]struct {
		made, wanted, kept []string
		add, remove        []string
	}{
		"nothing yet":               {nil, []string{"Ada", "Otto"}, nil, []string{"Ada", "Otto"}, nil},
		"in step":                   {[]string{"Ada"}, []string{"Ada"}, nil, nil, nil},
		"in step but for case":      {[]string{"ada"}, []string{"Ada"}, nil, nil, nil},
		"one lost the role":         {[]string{"Ada", "Otto"}, []string{"Ada"}, nil, nil, []string{"Otto"}},
		"one changed their account": {[]string{"Ada"}, []string{"Ada2"}, nil, []string{"Ada2"}, []string{"Ada"}},
		"set by hand as well":       {[]string{"Ada", "Otto"}, []string{"Ada"}, []string{"otto"}, nil, nil},
		"switched off":              {[]string{"Ada", "Otto"}, nil, nil, nil, []string{"Ada", "Otto"}},
		"made by hand, never by us": {nil, nil, []string{"Steve"}, nil, nil},
		"a new one beside the kept": {[]string{"Ada"}, []string{"Ada", "Otto"}, []string{"Steve"}, []string{"Otto"}, nil},
	} {
		add, remove := opChanges(c.made, c.wanted, c.kept)
		if !reflect.DeepEqual(add, c.add) || !reflect.DeepEqual(remove, c.remove) {
			t.Errorf("%s: add %v remove %v, want add %v remove %v", name, add, remove, c.add, c.remove)
		}
	}
}

func TestLinkingAMinecraftAccount(t *testing.T) {
	p := newPanel(t)
	p.srv.catalog.http.Transport = offline{
		"https://api.mojang.com/users/profiles/minecraft/steve": `{"id":"8667ba71b85a4004af54457a9734eed7","name":"Steve"}`,
	}
	linked := func(who string) any {
		_, me := p.get(who, "GET", "/me", "")
		return me["minecraft"]
	}
	if got := linked("operator"); got != nil {
		t.Fatalf("an account is linked before any was: %v", got)
	}
	// The name comes back as the account has it, with its UUID.
	code, got := p.get("operator", "PUT", "/me/minecraft", `{"name":" steve "}`)
	if code != 200 || got["name"] != "Steve" || got["uuid"] != "8667ba71b85a4004af54457a9734eed7" {
		t.Fatalf("linking: %d %v", code, got)
	}
	if account, _ := linked("operator").(map[string]any); account["name"] != "Steve" {
		t.Errorf("the link as stored: %v", linked("operator"))
	}
	// It shows beside the member, for the others in the organization.
	members := p.members("alpha")
	if members["operator"] != "Steve" || members["viewer"] != "" {
		t.Errorf("members with their players: %v", members)
	}
	// It is their own, and nobody else's to see on /me.
	if got := linked("admin"); got != nil {
		t.Errorf("one person's link shows on another's account: %v", got)
	}

	for name, c := range map[string]struct {
		body string
		want int
		code string
	}{
		"not a name":          {`{"name":"not a name!"}`, 400, "invalid"},
		"too long":            {`{"name":"abcdefghijklmnopq"}`, 400, "invalid"},
		"nobody of that name": {`{"name":"Herobrine"}`, 502, "upstream"},
	} {
		if got, code := p.do("operator", "PUT", "/me/minecraft", c.body); got != c.want || code != c.code {
			t.Errorf("%s: %d %s, want %d %s", name, got, code, c.want, c.code)
		}
	}
	if account, _ := linked("operator").(map[string]any); account["name"] != "Steve" {
		t.Errorf("a refused link replaced the one that was there: %v", linked("operator"))
	}

	if code, _ := p.do("operator", "DELETE", "/me/minecraft", ""); code != 204 {
		t.Errorf("unlinking: %d", code)
	}
	if got := linked("operator"); got != nil {
		t.Errorf("after unlinking: %v", got)
	}
}

func TestAutoOpSetting(t *testing.T) {
	p := newPanel(t)
	for value, want := range map[string]int{"admin": 200, "owner": 200, "off": 200, "operator": 200, "viewer": 400, "everyone": 400} {
		if got, code := p.do("admin", "PATCH", "/orgs/alpha/servers/world", `{"spec":{"autoOp":"`+value+`"}}`); got != want {
			t.Errorf("autoOp %q: %d %s, want %d", value, got, code, want)
		}
	}
}

// members returns each member's linked player by username.
func (p *panel) members(org string) map[string]string {
	p.t.Helper()
	var list []store.Member
	p.fetch("/orgs/"+org+"/members", &list)
	out := map[string]string{}
	for _, m := range list {
		out[m.Username] = m.MinecraftName
	}
	return out
}

// The note of who was made an operator is typed into a server's console
// name by name, and travels with snapshots. Only what could be a player's
// name is read out of it.
func TestSplitNamesDropsWhatIsNotAName(t *testing.T) {
	got := splitNames("Ada, Otto ,, bad name,x\nstop,@a,Steve;stop,ok_1")
	if want := "Ada Otto ok_1"; strings.Join(got, " ") != want {
		t.Errorf("names read: %q, want %q", strings.Join(got, " "), want)
	}
}

// An account that was renamed is followed by its UUID.
func TestRenamedAccountsAreFollowed(t *testing.T) {
	p := newPanel(t)
	p.srv.catalog.http.Transport = offline{
		"https://api.mojang.com/users/profiles/minecraft/steve":                                       `{"id":"8667ba71b85a4004af54457a9734eed7","name":"Steve"}`,
		"https://sessionserver.mojang.com/session/minecraft/profile/8667ba71b85a4004af54457a9734eed7": `{"id":"8667ba71b85a4004af54457a9734eed7","name":"Stefan"}`,
	}
	if code, _ := p.do("operator", "PUT", "/me/minecraft", `{"name":"steve"}`); code != 200 {
		t.Fatalf("linking: %d", code)
	}
	if code, _ := p.do("admin", "PUT", "/me/minecraft", `{"name":"steve"}`); code != 200 {
		t.Fatalf("linking: %d", code)
	}
	ctx := t.Context()
	alpha, err := p.srv.Store.OrgBySlug(ctx, "alpha")
	if err != nil {
		t.Fatal(err)
	}
	members, err := p.srv.Store.Members(ctx, alpha.ID)
	if err != nil {
		t.Fatal(err)
	}
	p.srv.refreshNames(ctx, members)
	for _, m := range members {
		if m.Username == "operator" && m.MinecraftName != "Stefan" {
			t.Errorf("the member in hand is still %q", m.MinecraftName)
		}
	}
	if got := p.members("alpha")["operator"]; got != "Stefan" {
		t.Errorf("the link as stored: %q, want Stefan", got)
	}
	// With Mojang not answering, the name is left as it is.
	p.srv.catalog.http.Transport = offline{}
	p.srv.catalog.entries = map[string]cacheEntry{}
	members, _ = p.srv.Store.Members(ctx, alpha.ID)
	p.srv.refreshNames(ctx, members)
	if got := p.members("alpha")["operator"]; got != "Stefan" {
		t.Errorf("after a failed lookup: %q, want it unchanged", got)
	}
}

// What a snapshot recorded of whom the panel put on a server's lists goes
// back on the server with its files, less anything that is not a name.
func TestNoteRoleLists(t *testing.T) {
	p := newPanel(t)
	ctx := t.Context()
	key := client.ObjectKey{Namespace: "mc-alpha", Name: "world"}
	notes := func() map[string]string {
		mc := &avsysv1.MinecraftServer{}
		if err := p.srv.Client.Get(ctx, key, mc); err != nil {
			t.Fatal(err)
		}
		return mc.Annotations
	}
	record := &snapshotRecord{AutoOps: "Ada, not a name,Otto", AutoWhitelist: "Vera,Ada;stop"}
	if err := p.srv.noteRoleLists(ctx, key, record.roleListNotes()); err != nil {
		t.Fatal(err)
	}
	if got := notes(); got[avsysv1.AnnotationAutoOps] != "Ada,Otto" || got[avsysv1.AnnotationAutoWhitelist] != "Vera" {
		t.Errorf("the notes put back: %v, want Ada,Otto and Vera", got)
	}
	// A snapshot from before anyone was put on them clears both.
	if err := p.srv.noteRoleLists(ctx, key, (&snapshotRecord{}).roleListNotes()); err != nil {
		t.Fatal(err)
	}
	got := notes()
	if _, ok := got[avsysv1.AnnotationAutoOps]; ok {
		t.Errorf("the operators note after restoring to none: %v", got)
	}
	if _, ok := got[avsysv1.AnnotationAutoWhitelist]; ok {
		t.Errorf("the whitelist note after restoring to none: %v", got)
	}
}

// Who is on a server's whitelist for being a member: nobody unless the
// server says from which role, and then also those who could not add anyone
// themselves.
func TestWhitelistByRole(t *testing.T) {
	members := []store.Member{
		{Username: "owner", Role: store.RoleOwner, MinecraftName: "Olive"},
		{Username: "admin", Role: store.RoleAdmin, MinecraftName: "Ada"},
		{Username: "operator", Role: store.RoleOperator, MinecraftName: "Otto"},
		{Username: "viewer", Role: store.RoleViewer, MinecraftName: "Vera"},
		{Username: "unlinked", Role: store.RoleViewer},
	}
	for setting, want := range map[string]string{
		"":         "",
		"off":      "",
		"viewer":   "Ada Olive Otto Vera",
		"operator": "Ada Olive Otto",
		"admin":    "Ada Olive",
		"owner":    "Olive",
	} {
		spec := &avsysv1.MinecraftServerSpec{AutoWhitelist: setting}
		if got := wantedFrom(whitelistRole(spec), members); strings.Join(got, " ") != want {
			t.Errorf("autoWhitelist %q: %q, want %q", setting, strings.Join(got, " "), want)
		}
	}
	// The two lists go by their own settings and their own kept names.
	spec := &avsysv1.MinecraftServerSpec{AutoOp: "owner", AutoWhitelist: "viewer", Ops: []string{"Steve"}, Whitelist: []string{"Friend"}}
	for _, list := range roleLists {
		wanted := wantedFrom(list.from(spec), members)
		kept := list.kept(spec)
		switch list.what {
		case "operators":
			if strings.Join(wanted, " ") != "Olive" || kept[0] != "Steve" || list.add != "op " || list.remove != "deop " {
				t.Errorf("operators: wanted %v kept %v", wanted, kept)
			}
		case "whitelist":
			if len(wanted) != 4 || kept[0] != "Friend" || list.add != "whitelist add " || list.remove != "whitelist remove " {
				t.Errorf("whitelist: wanted %v kept %v", wanted, kept)
			}
		default:
			t.Errorf("a list nobody has tested: %s", list.what)
		}
	}
	// A friend added by hand stays when a member who gave the same name leaves.
	if _, remove := opChanges([]string{"Friend", "Vera"}, nil, spec.Whitelist); len(remove) != 1 || remove[0] != "Vera" {
		t.Errorf("taken off the whitelist: %v, want only Vera", remove)
	}
}

func TestAutoWhitelistSetting(t *testing.T) {
	p := newPanel(t)
	for value, want := range map[string]int{"viewer": 200, "operator": 200, "admin": 200, "owner": 200, "off": 200, "everyone": 400, "member": 400} {
		if got, code := p.do("admin", "PATCH", "/orgs/alpha/servers/world", `{"spec":{"autoWhitelist":"`+value+`"}}`); got != want {
			t.Errorf("autoWhitelist %q: %d %s, want %d", value, got, code, want)
		}
	}
}

// The note of whom the panel put on a list stays true of the game when a
// command fails part of the way: what went through is noted, what did not
// is not.
func TestApplyNamesNotesWhatWentThrough(t *testing.T) {
	var ran []string
	failAt := func(n int) func(string) error {
		ran = nil
		return func(command string) error {
			ran = append(ran, command)
			if len(ran) == n {
				return errors.New("the server has gone")
			}
			return nil
		}
	}
	made, add, remove, wanted := []string{"Ada", "Otto"}, []string{"Vera", "Olive"}, []string{"Otto"}, []string{"Ada", "Olive", "Vera"}

	got, err := applyNames(made, add, remove, wanted, failAt(0), "whitelist add ", "whitelist remove ")
	if err != nil || strings.Join(got, " ") != "Ada Olive Vera" {
		t.Errorf("all through: %v, %v", got, err)
	}
	if strings.Join(ran, "; ") != "whitelist add Vera; whitelist add Olive; whitelist remove Otto" {
		t.Errorf("commands: %v", ran)
	}
	// The second addition fails: the first is noted, the removal not made.
	got, err = applyNames(made, add, remove, wanted, failAt(2), "whitelist add ", "whitelist remove ")
	if err == nil || strings.Join(got, " ") != "Ada Otto Vera" {
		t.Errorf("failing at the second command: %v, %v; want Ada Otto Vera noted", got, err)
	}
	// The removal fails: both additions are noted, and Otto still is.
	got, err = applyNames(made, add, remove, wanted, failAt(3), "whitelist add ", "whitelist remove ")
	if err == nil || strings.Join(got, " ") != "Ada Otto Vera Olive" {
		t.Errorf("failing at the removal: %v, %v", got, err)
	}
	// The very first fails: nothing has changed.
	got, err = applyNames(made, add, remove, wanted, failAt(1), "whitelist add ", "whitelist remove ")
	if err == nil || strings.Join(got, " ") != "Ada Otto" {
		t.Errorf("failing at once: %v, %v", got, err)
	}
	// What it was handed is not written over.
	if strings.Join(made, " ") != "Ada Otto" {
		t.Errorf("the note it started from was changed: %v", made)
	}
}
