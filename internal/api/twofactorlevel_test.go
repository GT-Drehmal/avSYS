package api

import (
	"context"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/GT-Drehmal/avSYS/internal/auth"
	"github.com/GT-Drehmal/avSYS/internal/auth/fakeidp"
	"github.com/GT-Drehmal/avSYS/internal/store"
)

// Who has to use two-factor, by role, for every way of signing in.

func (p *panel) setTwoFactorLevel(level string) {
	p.t.Helper()
	if err := p.srv.Store.SetSetting(context.Background(), store.SettingTwoFactorLevel, level); err != nil {
		p.t.Fatal(err)
	}
}

// linkProvider links someone here to login at the panel's provider.
func (p *panel) linkProvider(idp *fakeidp.IDP, user, login string) {
	p.t.Helper()
	id := store.Identity{Provider: "oidc", Subject: auth.OIDCSubject(idp.URL, login), UserID: p.users[user].ID, Login: login}
	if err := p.srv.Store.LinkIdentity(context.Background(), id); err != nil {
		p.t.Fatal(err)
	}
}

// viaProvider signs in through the panel's provider as login, from this
// tab, and returns where the panel sends it at the end.
func (b *tab) viaProvider(idp *fakeidp.IDP, login, query string) string {
	b.p.t.Helper()
	br := b.p.browser()
	br.cookies, br.addr = b.cookies, b.addr
	return br.signIn(idp, login, query)
}

// asToken makes a tab that calls with someone's API token.
func (p *panel) asToken(who string) *tab {
	b := p.tab(who + " token")
	b.header = map[string]string{"Authorization": "Bearer " + p.tokens[who]}
	return b
}

func (b *tab) me() map[string]any {
	b.p.t.Helper()
	var me map[string]any
	if got, code := b.call("GET", "/me", nil, &me); got != 200 {
		b.p.t.Fatalf("/me: %d %s", got, code)
	}
	return me
}

// held reports whether a tab is held to setting up two-factor.
func (b *tab) held() bool {
	b.p.t.Helper()
	got, code := b.call("GET", "/orgs/alpha", nil, nil)
	if got != 200 && code != "two_factor_required" {
		b.p.t.Fatalf("GET /orgs/alpha: %d %s", got, code)
	}
	return code == "two_factor_required"
}

// turnOnTwoFactorWithout sets two-factor up with no password, in a session
// just signed in, and returns the secret and the recovery codes.
func (b *tab) turnOnTwoFactorWithout() (string, []string) {
	b.p.t.Helper()
	var setup struct{ Secret string }
	if got, code := b.call("POST", "/me/two-factor/setup", map[string]string{}, &setup); got != 200 {
		b.p.t.Fatalf("setting up two-factor without a password: %d %s", got, code)
	}
	var done struct{ RecoveryCodes []string }
	if got, code := b.call("POST", "/me/two-factor", map[string]string{"code": b.p.codeOf(setup.Secret, 0)}, &done); got != 200 {
		b.p.t.Fatalf("confirming two-factor: %d %s", got, code)
	}
	return setup.Secret, done.RecoveryCodes
}

// atCodeStep checks that the panel sent a sign-in through a provider on
// to the code, for next, with no session yet.
func atCodeStep(t *testing.T, b *tab, to, next string) {
	t.Helper()
	u, err := url.Parse(to)
	if err != nil || u.Path != "/login" || u.Query().Get("step") != "code" || u.Query().Get("next") != next {
		t.Errorf("sent to %q, want the code step for %s", to, next)
	}
	if b.cookies[challengeCookie] == "" || b.signedIn() {
		t.Error("no sign-in waits for its code, or one is signed in without it")
	}
}

func TestWhoHasToUseTwoFactor(t *testing.T) {
	p := newPanel(t)
	ctx := context.Background()
	loner := &store.User{Username: "loner"}
	if err := p.srv.Store.CreateUser(ctx, loner); err != nil {
		t.Fatal(err)
	}
	p.users["loner"] = loner
	// alpha: owner, admin, operator, viewer; beta: outsider, its owner;
	// root, the panel's administrator, in neither; loner in none.
	want := map[string]map[string]bool{
		store.TwoFactorNobody:    {},
		store.TwoFactorAdmins:    {"root": true, "owner": true, "admin": true, "outsider": true},
		store.TwoFactorOperators: {"root": true, "owner": true, "admin": true, "outsider": true, "operator": true},
		store.TwoFactorEveryone:  {"root": true, "owner": true, "admin": true, "outsider": true, "operator": true, "viewer": true, "loner": true},
	}
	check := func(level string, want map[string]bool) {
		t.Helper()
		p.setTwoFactorLevel(level)
		for name, u := range p.users {
			got, err := p.srv.twoFactorRequired(ctx, u)
			if err != nil {
				t.Fatal(err)
			}
			if got != want[name] {
				t.Errorf("%s, level %s: required %v, want %v", name, level, got, want[name])
			}
		}
	}
	for level, who := range want {
		check(level, who)
	}

	// The highest role in any organization counts: the viewer of alpha is
	// an admin of beta, and the operator of alpha a viewer there.
	beta, err := p.srv.Store.OrgBySlug(ctx, "beta")
	if err != nil {
		t.Fatal(err)
	}
	p.join(beta, "viewer", store.RoleAdmin)
	p.join(beta, "operator", store.RoleViewer)
	check(store.TwoFactorAdmins, map[string]bool{"root": true, "owner": true, "admin": true, "outsider": true, "viewer": true})
	check(store.TwoFactorOperators, map[string]bool{"root": true, "owner": true, "admin": true, "outsider": true, "viewer": true, "operator": true})

	// A value that is not one is taken as admins and owners.
	p.setTwoFactorLevel("bogus")
	if level, err := p.srv.twoFactorLevel(ctx); err != nil || level != store.TwoFactorAdmins {
		t.Errorf("level of a bogus value: %q %v", level, err)
	}
}

func TestSettingWhoHasToUseTwoFactor(t *testing.T) {
	p := newPanel(t)
	for _, who := range []string{"owner", "admin", "viewer"} {
		if got, _ := p.do(who, "PUT", "/settings/two-factor", `{"level":"everyone"}`); got != 403 {
			t.Errorf("%s setting it: %d, want 403", who, got)
		}
	}
	root := p.asToken("root")
	for _, bad := range []string{"", "admin", "Everyone", "owners"} {
		if got, _ := root.call("PUT", "/settings/two-factor", map[string]string{"level": bad}, nil); got != 400 {
			t.Errorf("level %q: %d, want 400", bad, got)
		}
	}
	for _, level := range []string{"admins", "operators", "everyone", "nobody"} {
		if got, code := root.call("PUT", "/settings/two-factor", map[string]string{"level": level}, nil); got != 204 {
			t.Fatalf("level %s: %d %s", level, got, code)
		}
		if me := p.asToken("viewer").me(); me["twoFactorLevel"] != level {
			t.Errorf("/me says level %v, want %s", me["twoFactorLevel"], level)
		}
	}
	// It is the whole panel's, with passwords off too.
	p.srv.Config.NoPasswords = true
	if got, code := root.call("PUT", "/settings/two-factor", map[string]string{"level": "admins"}, nil); got != 204 {
		t.Errorf("with passwords off: %d %s", got, code)
	}
}

// A password sign-in of someone who has to use two-factor and has not set
// it up gets a session held to setting it up; then it asks for the code.
func TestPasswordSignInOfSomeoneWhoHasToUseTwoFactor(t *testing.T) {
	p := newPanel(t)
	p.withPasswords()
	p.setTwoFactorLevel(store.TwoFactorAdmins)
	admin, viewer := p.tab("admin"), p.tab("viewer")
	admin.signIn("admin", "")
	viewer.signIn("viewer", "")
	if viewer.held() {
		t.Error("a viewer was held with admins and owners having to")
	}
	if !admin.held() {
		t.Fatal("an organization's admin was not held to setting up two-factor")
	}
	me := admin.me()
	if me["mustSetUpTwoFactor"] != true || me["twoFactorRequired"] != true || me["twoFactorLevel"] != "admins" {
		t.Errorf("/me: %v", me)
	}
	for _, route := range [][2]string{{"POST", "/orgs"}, {"GET", "/me/sessions"}, {"POST", "/me/password"}, {"POST", "/me/tokens"}, {"PUT", "/settings/two-factor"}} {
		if got, code := admin.call(route[0], route[1], map[string]any{}, nil); got != 403 || code != "two_factor_required" {
			t.Errorf("%s %s before setting up two-factor: %d %s", route[0], route[1], got, code)
		}
	}
	secret, _ := admin.turnOnTwoFactor()
	if admin.held() || admin.me()["mustSetUpTwoFactor"] != false {
		t.Error("still held after setting two-factor up")
	}
	// It cannot be turned off while it has to be on.
	if got, code := admin.call("POST", "/me/two-factor/off", map[string]string{"password": testPassword}, nil); got != 409 || code != "required" {
		t.Errorf("turning it off while required: %d %s", got, code)
	}
	// The next sign-in asks for the code.
	p.tab("again").signIn("admin", secret)
	// With nobody having to, it can be turned off.
	p.setTwoFactorLevel(store.TwoFactorNobody)
	if got, _ := admin.call("POST", "/me/two-factor/off", map[string]string{"password": testPassword}, nil); got != 204 {
		t.Errorf("turning it off once not required: %d", got)
	}
}

// The panel's administrator has to, whenever anyone does.
func TestThePanelsAdministratorHasToUseTwoFactor(t *testing.T) {
	p := newPanel(t)
	p.withPasswords()
	root := p.tab("root")
	root.signIn("root", "")
	if root.held() {
		t.Error("held with nobody having to")
	}
	for _, level := range []string{store.TwoFactorAdmins, store.TwoFactorOperators, store.TwoFactorEveryone} {
		p.setTwoFactorLevel(level)
		if !root.held() {
			t.Errorf("the administrator, in no organization, not held with %s having to", level)
		}
	}
}

// Signing in through a provider is held to setting two-factor up just as
// a password is, which it is then without a password, and asks for the
// code after the provider from then on.
func TestProviderSignInOfSomeoneWhoHasToUseTwoFactor(t *testing.T) {
	p := newPanel(t)
	idp := withIDP(t, p)
	p.setTwoFactorLevel(store.TwoFactorAdmins)
	p.linkProvider(idp, "admin", "ada")

	b := p.tab("ada")
	if to := b.viaProvider(idp, "ada", "next=/o/alpha"); to != "/o/alpha" {
		t.Errorf("sent to %q, want /o/alpha", to)
	}
	if !b.held() {
		t.Fatal("an admin signed in through a provider was not held to setting up two-factor")
	}
	me := b.me()
	if me["mustSetUpTwoFactor"] != true || me["hasPassword"] != false || me["recentSignIn"] != true {
		t.Errorf("/me: %v", me)
	}
	secret, recovery := b.turnOnTwoFactorWithout()
	if b.held() {
		t.Fatal("held after setting two-factor up")
	}

	// The next sign-in through the provider asks for the code.
	c := p.tab("again")
	atCodeStep(t, c, c.viaProvider(idp, "ada", "next=/o/alpha"), "/o/alpha")
	if got, code := c.call("POST", "/auth/login/code", map[string]string{"code": "000000"}, nil); got != 400 || code != "wrong_code" {
		t.Errorf("a wrong code: %d %s", got, code)
	}
	if got, code := c.call("POST", "/auth/login/code", map[string]string{"code": p.codeOf(secret, 1)}, nil); got != 200 {
		t.Fatalf("the code: %d %s", got, code)
	}
	if !c.signedIn() || c.held() || c.cookies[challengeCookie] != "" {
		t.Error("the code did not sign in, or left the sign-in waiting")
	}
	for _, s := range c.sessions() {
		if s.Current && s.Method != store.SignedInWithProvider {
			t.Errorf("the session is of %q, want provider", s.Method)
		}
	}

	// A recovery code does too, once.
	d := p.tab("recovering")
	atCodeStep(t, d, d.viaProvider(idp, "ada", ""), "/")
	if got, code := d.call("POST", "/auth/login/code", map[string]string{"code": recovery[0]}, nil); got != 200 {
		t.Fatalf("a recovery code: %d %s", got, code)
	}
	e := p.tab("recovering again")
	e.viaProvider(idp, "ada", "")
	if got, _ := e.call("POST", "/auth/login/code", map[string]string{"code": recovery[0]}, nil); got != 400 {
		t.Errorf("a recovery code used twice: %d, want 400", got)
	}

	// Too many wrong codes, and the provider is asked again.
	wrong := p.tab("wrong")
	wrong.viaProvider(idp, "ada", "")
	for i := 1; i < challengeTries; i++ {
		wrong.call("POST", "/auth/login/code", map[string]string{"code": "000000"}, nil)
	}
	if got, code := wrong.call("POST", "/auth/login/code", map[string]string{"code": "000000"}, nil); got != 401 || code != "sign_in_again" {
		t.Errorf("wrong code %d: %d %s, want sign_in_again", challengeTries, got, code)
	}
}

// Anyone with two-factor on is asked for the code after a provider, as
// after a password, whether or not they have to have it.
func TestProviderSignInAsksAnyoneWithTwoFactorForTheCode(t *testing.T) {
	p := newPanel(t)
	idp := withIDP(t, p)
	p.linkProvider(idp, "viewer", "vee")
	b := p.tab("vee")
	b.viaProvider(idp, "vee", "")
	if b.held() {
		t.Fatal("held with nobody having to")
	}
	secret, _ := b.turnOnTwoFactorWithout()
	c := p.tab("again")
	atCodeStep(t, c, c.viaProvider(idp, "vee", "next=/account"), "/account")
	if got, code := c.call("POST", "/auth/login/code", map[string]string{"code": p.codeOf(secret, 1)}, nil); got != 200 || !c.signedIn() {
		t.Errorf("the code: %d %s", got, code)
	}
}

// An invite someone signs in with is taken only once they are in: after
// the code, for an account with two-factor.
func TestAnInviteIsTakenAfterTheCode(t *testing.T) {
	p := newPanel(t)
	ctx := context.Background()
	idp := withIDP(t, p)
	p.linkProvider(idp, "viewer", "vee")
	b := p.tab("vee")
	b.viaProvider(idp, "vee", "")
	secret, _ := b.turnOnTwoFactorWithout()
	beta, err := p.srv.Store.OrgBySlug(ctx, "beta")
	if err != nil {
		t.Fatal(err)
	}
	inv := &store.Invite{OrgID: beta.ID, Role: store.RoleOperator, CreatedBy: "test"}
	if err := p.srv.Store.CreateInvite(ctx, inv); err != nil {
		t.Fatal(err)
	}
	c := p.tab("invited")
	atCodeStep(t, c, c.viaProvider(idp, "vee", "invite="+inv.Token+"&next=/o/beta"), "/o/beta")
	if _, err := p.srv.Store.RoleIn(ctx, beta.ID, p.users["viewer"].ID); err == nil {
		t.Fatal("the invite was taken before the code")
	}
	if got, code := c.call("POST", "/auth/login/code", map[string]string{"code": p.codeOf(secret, 1)}, nil); got != 200 {
		t.Fatalf("the code: %d %s", got, code)
	}
	if role, err := p.srv.Store.RoleIn(ctx, beta.ID, p.users["viewer"].ID); err != nil || role != store.RoleOperator {
		t.Errorf("role in beta after the code: %q %v", role, err)
	}
}

// Someone with no password sets two-factor up, turns it off and makes new
// recovery codes with a recent sign-in in its place; signing in again
// through the provider, from the same browser, makes one.
func TestTwoFactorWithoutAPassword(t *testing.T) {
	p := newPanel(t)
	idp := withIDP(t, p)
	p.linkProvider(idp, "viewer", "vee")
	b := p.tab("vee")
	b.viaProvider(idp, "vee", "")
	if len(b.sessions()) != 1 {
		t.Fatal("not one session")
	}

	// Signed in a while ago, it is not enough.
	was := recentSignIn
	recentSignIn = 0
	if b.me()["recentSignIn"] != false {
		t.Error("/me says the sign-in is recent")
	}
	if got, code := b.call("POST", "/me/two-factor/setup", map[string]string{}, nil); got != 403 || code != "recent_sign_in" {
		t.Errorf("setting up long after signing in: %d %s", got, code)
	}
	recentSignIn = was

	// Signing in again from this browser makes it recent, in place of the
	// session there was.
	b.viaProvider(idp, "vee", "next=/account")
	if list := b.sessions(); len(list) != 1 || !list[0].Current {
		t.Errorf("sessions after signing in again: %+v", list)
	}
	secret, _ := b.turnOnTwoFactorWithout()

	// Nor is an API token, which has no sign-in.
	if got, code := p.asToken("viewer").call("POST", "/me/two-factor/recovery-codes", map[string]string{}, nil); got != 403 || code != "recent_sign_in" {
		t.Errorf("new recovery codes with an API token: %d %s", got, code)
	}

	recentSignIn = 0
	for _, path := range []string{"/me/two-factor/off", "/me/two-factor/recovery-codes"} {
		if got, code := b.call("POST", path, map[string]string{}, nil); got != 403 || code != "recent_sign_in" {
			t.Errorf("%s long after signing in: %d %s", path, got, code)
		}
	}
	recentSignIn = was

	// Signing in again, with two-factor on, asks for the code: the session
	// before has ended.
	atCodeStep(t, b, b.viaProvider(idp, "vee", "next=/account"), "/account")
	if got, code := b.call("POST", "/auth/login/code", map[string]string{"code": p.codeOf(secret, 1)}, nil); got != 200 {
		t.Fatalf("the code: %d %s", got, code)
	}
	var fresh struct{ RecoveryCodes []string }
	if got, code := b.call("POST", "/me/two-factor/recovery-codes", map[string]string{}, &fresh); got != 200 || len(fresh.RecoveryCodes) != 10 {
		t.Errorf("new recovery codes: %d %s", got, code)
	}
	if got, code := b.call("POST", "/me/two-factor/off", map[string]string{}, nil); got != 204 {
		t.Errorf("turning it off: %d %s", got, code)
	}

	// Someone with a password gives it, however they signed in.
	p.withPasswords()
	b.viaProvider(idp, "vee", "")
	if got, code := b.call("POST", "/me/two-factor/setup", map[string]string{}, nil); got != 400 || code != "wrong_password" {
		t.Errorf("with a password, setting up without it: %d %s", got, code)
	}
}

// Someone who comes to have to use two-factor, made an admin or by the
// level being raised, is held at their next request; API tokens are not,
// but no new one is made until it is set up.
func TestComingToHaveToUseTwoFactor(t *testing.T) {
	p := newPanel(t)
	p.withPasswords()
	p.setTwoFactorLevel(store.TwoFactorAdmins)
	viewer, operator := p.tab("viewer"), p.tab("operator")
	viewer.signIn("viewer", "")
	operator.signIn("operator", "")
	if viewer.held() || operator.held() {
		t.Fatal("held before having to")
	}

	// The viewer is made an admin.
	if got, code := p.do("owner", "PATCH", "/orgs/alpha/members/"+p.users["viewer"].ID, `{"role":"admin"}`); got != 204 {
		t.Fatalf("making the viewer an admin: %d %s", got, code)
	}
	if !viewer.held() {
		t.Error("a member made an admin was not held")
	}
	if viewer.me()["mustSetUpTwoFactor"] != true {
		t.Error("/me does not say to set it up")
	}
	// Their API token still works, and makes no new one.
	if got, _ := p.do("viewer", "GET", "/orgs/alpha", ""); got != 200 {
		t.Errorf("their API token: %d, want 200", got)
	}
	if got, code := p.asToken("viewer").call("POST", "/me/tokens", map[string]string{"name": "more"}, nil); got != 403 || code != "two_factor_required" {
		t.Errorf("a new token, without two-factor: %d %s", got, code)
	}
	viewer.turnOnTwoFactor()
	if got, code := viewer.call("POST", "/me/tokens", map[string]string{"name": "more"}, nil); got != 201 {
		t.Errorf("a new token, with two-factor: %d %s", got, code)
	}

	// The level is raised to operators.
	if operator.held() {
		t.Fatal("the operator held before the level was raised")
	}
	if got, _ := p.do("root", "PUT", "/settings/two-factor", `{"level":"operators"}`); got != 204 {
		t.Fatal(got)
	}
	if !operator.held() {
		t.Error("the operator was not held once operators had to")
	}
}

// A reset link that turns two-factor off does only that for someone with
// no password, who then sets it up again if they have to.
func TestResetLinkForAProviderAccount(t *testing.T) {
	p := newPanel(t)
	ctx := context.Background()
	idp := withIDP(t, p)
	p.setTwoFactorLevel(store.TwoFactorAdmins)
	p.linkProvider(idp, "admin", "ada")
	b := p.tab("ada")
	b.viaProvider(idp, "ada", "")
	b.turnOnTwoFactorWithout()
	adminID := p.users["admin"].ID
	root := p.asToken("root")

	// A link that only sets a password asks for one.
	var link struct{ URL string }
	if got, code := root.call("POST", "/users/"+adminID+"/reset-link", map[string]any{}, &link); got != 201 {
		t.Fatalf("making a link: %d %s", got, code)
	}
	var what map[string]any
	user := p.tab("user")
	token := strings.TrimPrefix(link.URL, "http://panel.test/reset/")
	if got, _ := user.call("GET", "/reset-links/"+token, nil, &what); got != 200 || what["newPassword"] != true || what["twoFactorOff"] != false {
		t.Errorf("a link to set a password: %d %v", got, what)
	}
	if got, _ := user.call("POST", "/reset-links/"+token, map[string]string{}, nil); got != 400 {
		t.Errorf("using it with no password: %d, want 400", got)
	}

	// One that turns two-factor off does only that.
	if got, code := root.call("POST", "/users/"+adminID+"/reset-link", map[string]any{"twoFactorOff": true}, &link); got != 201 {
		t.Fatalf("making a link: %d %s", got, code)
	}
	token = strings.TrimPrefix(link.URL, "http://panel.test/reset/")
	if got, _ := user.call("GET", "/reset-links/"+token, nil, &what); got != 200 || what["newPassword"] != false || what["twoFactorOff"] != true {
		t.Errorf("a link to turn two-factor off: %d %v", got, what)
	}
	if got, code := user.call("POST", "/reset-links/"+token, map[string]string{}, nil); got != 200 {
		t.Fatalf("using it: %d %s", got, code)
	}
	if tf, _ := p.srv.Store.TwoFactor(ctx, adminID); tf.Secret != "" || tf.RecoveryCodes != 0 {
		t.Errorf("two-factor after the link: %+v", tf)
	}
	if u, _ := p.srv.Store.User(ctx, adminID); u.PasswordHash != "" {
		t.Error("the link gave a password")
	}
	if b.signedIn() {
		t.Error("still signed in after the link was used")
	}
	// Signing in again takes no code, and holds them to setting it up.
	c := p.tab("after")
	if to := c.viaProvider(idp, "ada", ""); to != "/" || !c.held() {
		t.Errorf("after the reset: sent to %q, held %v", to, c.held())
	}
}

// With passwords off, two-factor is had through the provider as with
// passwords on: set up with a recent sign-in, asked for after the
// provider. Reset links can only turn it off.
func TestPasswordsOffKeepsTwoFactor(t *testing.T) {
	p := newPanel(t)
	ctx := context.Background()
	p.withPasswords()
	idp := withIDP(t, p)
	p.linkProvider(idp, "root", "rooty")
	p.linkProvider(idp, "viewer", "vee")
	viewerID := p.users["viewer"].ID
	root := p.asToken("root")
	var before struct{ URL string }
	if got, code := root.call("POST", "/users/"+viewerID+"/reset-link", map[string]any{}, &before); got != 201 {
		t.Fatalf("with passwords on, making a reset link: %d %s", got, code)
	}
	p.srv.Config.NoPasswords = true
	p.setTwoFactorLevel(store.TwoFactorAdmins)

	// The administrator is held, and sets it up without the password they
	// have from before.
	a := p.tab("rooty")
	a.viaProvider(idp, "rooty", "")
	if !a.held() {
		t.Fatal("the administrator was not held with passwords off")
	}
	var setup struct{ Secret string }
	if got, code := a.call("POST", "/me/two-factor/setup", map[string]string{"password": "not looked at"}, &setup); got != 200 {
		t.Fatalf("setting up with passwords off: %d %s", got, code)
	}
	if got, code := a.call("POST", "/me/two-factor", map[string]string{"code": p.codeOf(setup.Secret, 0)}, nil); got != 200 || a.held() {
		t.Fatalf("confirming: %d %s", got, code)
	}
	n := p.tab("next")
	atCodeStep(t, n, n.viaProvider(idp, "rooty", ""), "/")
	if got, code := n.call("POST", "/auth/login/code", map[string]string{"code": p.codeOf(setup.Secret, 1)}, nil); got != 200 || !n.signedIn() {
		t.Errorf("the code, with passwords off: %d %s", got, code)
	}

	// The viewer, with two-factor on, lost their phone.
	v := p.tab("vee")
	v.viaProvider(idp, "vee", "")
	v.turnOnTwoFactorWithout()
	if got, code := root.call("POST", "/users/"+viewerID+"/reset-link", map[string]any{}, nil); got != 403 || code != "passwords_off" {
		t.Errorf("a link to set a password, with passwords off: %d %s", got, code)
	}
	oldToken := strings.TrimPrefix(before.URL, "http://panel.test/reset/")
	if got, code := p.tab("y").call("GET", "/reset-links/"+oldToken, nil, nil); got != 403 || code != "passwords_off" {
		t.Errorf("a link from before, to set a password: %d %s", got, code)
	}
	var link struct{ URL string }
	if got, code := root.call("POST", "/users/"+viewerID+"/reset-link", map[string]any{"twoFactorOff": true}, &link); got != 201 {
		t.Fatalf("a link to turn two-factor off: %d %s", got, code)
	}
	token := strings.TrimPrefix(link.URL, "http://panel.test/reset/")
	var what map[string]any
	if got, _ := p.tab("y").call("GET", "/reset-links/"+token, nil, &what); got != 200 || what["newPassword"] != false {
		t.Errorf("opening it: %d %v", got, what)
	}
	if got, code := p.tab("y").call("POST", "/reset-links/"+token, map[string]string{"password": "a brand new password"}, nil); got != 200 {
		t.Fatalf("using it: %d %s", got, code)
	}
	if tf, _ := p.srv.Store.TwoFactor(ctx, viewerID); tf.Secret != "" {
		t.Error("two-factor is still on")
	}
	if u, _ := p.srv.Store.User(ctx, viewerID); !auth.CheckPassword(u.PasswordHash, testPassword) {
		t.Error("the password was changed with passwords off")
	}
	// Passwords themselves stay refused.
	if got, code := p.tab("z").call("POST", "/auth/login", map[string]string{"username": "viewer", "password": testPassword}, nil); got != 403 || code != "passwords_off" {
		t.Errorf("a password sign-in: %d %s", got, code)
	}
}

// A panel just installed has admins and owners having to: its first
// account, the administrator, sets two-factor up first.
func TestAPanelJustInstalledAsksForTwoFactor(t *testing.T) {
	p := newPasswordlessPanel(t)
	p.codeTime = codeStep.Add(15 * time.Second)
	p.srv.TwoFactorClock = func() time.Time { return p.codeTime }
	idp := withIDP(t, p)
	b := p.browser()
	b.signIn(idp, "boss", "setup="+setupCode)
	boss := p.tab("boss")
	boss.cookies = b.cookies
	if level, _ := p.srv.twoFactorLevel(context.Background()); level != store.TwoFactorAdmins {
		t.Errorf("level %q, want admins", level)
	}
	if got, code := boss.call("GET", "/me/sessions", nil, nil); got != 403 || code != "two_factor_required" {
		t.Errorf("the first account, before setting up two-factor: %d %s", got, code)
	}
	boss.turnOnTwoFactorWithout()
	if got, code := boss.call("GET", "/me/sessions", nil, nil); got != 200 {
		t.Errorf("after: %d %s", got, code)
	}
}
