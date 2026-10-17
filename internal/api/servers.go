package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"

	jsonpatch "gopkg.in/evanphx/json-patch.v4"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	avsysv1 "github.com/GT-Drehmal/avSYS/api/v1alpha1"
	"github.com/GT-Drehmal/avSYS/internal/backup"
	"github.com/GT-Drehmal/avSYS/internal/controller"
	"github.com/GT-Drehmal/avSYS/internal/rcon"
	"github.com/GT-Drehmal/avSYS/internal/router"
	"github.com/GT-Drehmal/avSYS/internal/store"
)

// serverView is how a MinecraftServer is presented over the API. Spec and
// status are the resource's own, so the API, kubectl and GitOps manifests all
// share one vocabulary.
type serverView struct {
	Org       string    `json:"org"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"createdAt"`
	// Revision increases with every change to the resource. Clients use it
	// to discard an update that arrives after a newer one.
	Revision string `json:"revision"`
	// Java is what the game version ships with and what the server runs on,
	// which is higher when installed content needs more. Zero when unknown.
	Java   javaView                      `json:"java"`
	Spec   avsysv1.MinecraftServerSpec   `json:"spec"`
	Status avsysv1.MinecraftServerStatus `json:"status"`
}

type javaView struct {
	Game      int `json:"game"`
	Effective int `json:"effective"`
}

func (s *Server) view(mc *avsysv1.MinecraftServer) serverView {
	org, _ := s.orgOfNamespace(mc.Namespace)
	v := serverView{Org: org, Name: mc.Name, CreatedAt: mc.CreationTimestamp.Time, Revision: mc.ResourceVersion, Spec: mc.Spec, Status: mc.Status}
	v.Java.Game, v.Java.Effective = controller.JavaOf(&mc.Spec)
	return v
}

var (
	serverNamePattern = regexp.MustCompile(`^[a-z]([a-z0-9-]{0,38}[a-z0-9])?$`)
	hostnamePattern   = regexp.MustCompile(`^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]{2,}$`)

	minMemory  = resource.MustParse("512Mi")
	minStorage = resource.MustParse("1Gi")
)

// applyDefaults fills in what a request left out. The CRD schema carries the
// same defaults for resources created with kubectl.
func (s *Server) applyDefaults(org, name string, spec *avsysv1.MinecraftServerSpec) {
	if spec.State == "" {
		spec.State = avsysv1.StateRunning
	}
	if spec.Version == "" {
		spec.Version = "LATEST"
	}
	if spec.Loader == "" {
		spec.Loader = avsysv1.LoaderVanilla
	}
	if spec.Resources.Memory.IsZero() {
		spec.Resources.Memory = resource.MustParse("4Gi")
	}
	if spec.Storage.Size.IsZero() {
		spec.Storage.Size = resource.MustParse("10Gi")
	}
	if spec.Expose.Type == "" {
		spec.Expose.Type = avsysv1.ExposeRouter
	}
	if spec.Expose.Type == avsysv1.ExposeRouter && spec.Expose.Hostname == "" && s.Config.RouterDomain != "" {
		spec.Expose.Hostname = fmt.Sprintf("%s.%s.%s", name, org, s.Config.RouterDomain)
	}
	if spec.Sleep.IdleMinutes == 0 {
		spec.Sleep.IdleMinutes = 15
	}
	for i := range spec.Mods {
		if spec.Mods[i].Kind == "" {
			spec.Mods[i].Kind = avsysv1.KindMod
		}
	}
}

// collectors picks a garbage collector, which Aikar's flags pick already.
// Switching G1 off is the same.
var collectors = regexp.MustCompile(`^-XX:(\+Use(ZGC|ShenandoahGC|ParallelGC|ParallelOldGC|ParNewGC|SerialGC|EpsilonGC|ConcMarkSweepGC)|-UseG1GC)$`)

// heapFlags set the heap, by any of the names HotSpot takes for it.
var heapFlags = regexp.MustCompile(`^(-Xm[xs]|-XX:(Max|Initial|Min)HeapSize=|-XX:(Max|Initial|Min)RAMPercentage=|-XX:MaxRAM=)`)

// checkJVM checks extra JVM flags, and tidies them to one per line. They are
// options for Java and nothing else: no heap size, which is the server's
// memory, and no second collector on top of Aikar's.
func checkJVM(jvm *avsysv1.JVMSpec) error {
	flags := strings.Fields(jvm.ExtraFlags)
	if len(jvm.ExtraFlags) > 2048 {
		return errBadRequest("Extra JVM flags are up to 2048 characters.")
	}
	aikar := jvm.AikarFlags != nil && *jvm.AikarFlags
	for _, f := range flags {
		switch {
		case !strings.HasPrefix(f, "-"):
			return errBadRequest("%q is not a JVM option: each starts with a dash, as -XX:+AlwaysPreTouch.", f)
		case heapFlags.MatchString(f):
			return errBadRequest("%s sets the heap, which is the server's memory. Change Memory instead.", f)
		case aikar && collectors.MatchString(f):
			return errBadRequest("%s picks a garbage collector, and Aikar's flags pick G1. Switch Aikar's flags off to use another.", f)
		}
	}
	jvm.ExtraFlags = strings.Join(flags, "\n")
	return nil
}

// tidyPicks keeps, of the members picked one by one for a server's lists,
// those who are members now, each once. One both put on a list and left off
// it is put on. None left is the same as none picked.
func (s *Server) tidyPicks(ctx context.Context, namespace string, spec *avsysv1.MinecraftServerSpec) error {
	if spec.OpMembers == nil && spec.WhitelistMembers == nil {
		return nil
	}
	slug, _ := s.orgOfNamespace(namespace)
	org, err := s.Store.OrgBySlug(ctx, slug)
	if err != nil {
		return err
	}
	members, err := s.Store.Members(ctx, org.ID)
	if err != nil {
		return err
	}
	member := map[string]bool{}
	for _, m := range members {
		member[m.UserID] = true
	}
	tidy := func(picks *avsysv1.MemberPicks) *avsysv1.MemberPicks {
		if picks == nil {
			return nil
		}
		seen := map[string]bool{}
		keep := func(ids []string) []string {
			var out []string
			for _, id := range ids {
				if member[id] && !seen[id] {
					seen[id] = true
					out = append(out, id)
				}
			}
			return out
		}
		out := &avsysv1.MemberPicks{Also: keep(picks.Also)}
		out.Except = keep(picks.Except)
		if len(out.Also) == 0 && len(out.Except) == 0 {
			return nil
		}
		return out
	}
	spec.OpMembers, spec.WhitelistMembers = tidy(spec.OpMembers), tidy(spec.WhitelistMembers)
	return nil
}

func (s *Server) validateSpec(ctx context.Context, namespace, name string, spec *avsysv1.MinecraftServerSpec) error {
	if !spec.EULA {
		return errBadRequest("Accept the Minecraft EULA to run a server.")
	}
	if spec.Resources.Memory.Cmp(minMemory) < 0 {
		return errBadRequest("A server needs at least 512Mi of memory.")
	}
	if spec.Storage.Size.Cmp(minStorage) < 0 {
		return errBadRequest("A server needs at least 1Gi of storage.")
	}
	if err := checkJVM(&spec.JVM); err != nil {
		return err
	}
	if err := packFits(spec); err != nil {
		return err
	}
	if err := checkPorts(spec.Ports); err != nil {
		return err
	}
	spec.Expose.Hostname = router.NormalizeHostname(strings.TrimSpace(spec.Expose.Hostname))
	if host := spec.Expose.Hostname; host != "" {
		if !hostnamePattern.MatchString(host) {
			return errBadRequest("%q is not a valid hostname.", host)
		}
		if spec.Expose.Type == avsysv1.ExposeRouter {
			var same avsysv1.MinecraftServerList
			if err := s.Client.List(ctx, &same, client.MatchingFields{router.HostnameIndex: host}); err != nil {
				return err
			}
			for _, other := range same.Items {
				if other.Namespace != namespace || other.Name != name {
					return &Error{Status: http.StatusConflict, Code: "hostname_taken", Message: fmt.Sprintf("Another server already uses %s.", host)}
				}
			}
		}
	}
	seen := map[string]bool{}
	for _, m := range spec.Mods {
		if strings.TrimSpace(m.Project) == "" {
			return errBadRequest("A mod entry is missing its project.")
		}
		if m.Source == avsysv1.SourceUpload {
			// Uploads are named by the upload endpoint; anything else here
			// would let a path reach outside the content directory.
			if name, err := contentFilename(m.Project, m.Kind); err != nil || name != m.Project {
				return errBadRequest("%q is not an uploaded file.", m.Project)
			}
		}
		key := string(m.Source) + "/" + m.Project
		if seen[key] {
			return errBadRequest("%s is listed twice.", m.Project)
		}
		seen[key] = true
	}
	if rp := spec.ResourcePack; rp != nil && rp.URL != "" && !strings.HasPrefix(rp.URL, "http://") && !strings.HasPrefix(rp.URL, "https://") {
		return errBadRequest("The resource pack URL must start with http:// or https://.")
	}
	if rp := spec.ResourcePack; rp != nil && rp.Project != nil {
		if rp.Project.Source != "modrinth" || rp.Project.ID == "" {
			return errBadRequest("A resource pack picked from a project names it: source modrinth, and its id.")
		}
	}
	switch spec.AutoOp {
	case "", "off", string(store.RoleOperator), string(store.RoleAdmin), string(store.RoleOwner):
	default:
		return errBadRequest("Operators in game go by role: operator, admin, owner, or off. %q is none of those.", spec.AutoOp)
	}
	switch spec.AutoWhitelist {
	case "", "off", string(store.RoleViewer), string(store.RoleOperator), string(store.RoleAdmin), string(store.RoleOwner):
	default:
		return errBadRequest("The whitelist goes by role: viewer for every member, operator, admin, owner, or off. %q is none of those.", spec.AutoWhitelist)
	}
	if err := s.tidyPicks(ctx, namespace, spec); err != nil {
		return err
	}
	if spec.Backups != nil {
		// Held to the limits an organization's own schedule is.
		if err := policyFor(defaultPolicy, spec).validate(); err != nil {
			return err
		}
	}
	return validateProperties(spec.Properties)
}

var propertyNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// validateProperties keeps every entry to one line. They reach the server as
// name=value lines, so a line break in one would add an entry of its own.
func validateProperties(properties map[string]string) error {
	names := make([]string, 0, len(properties))
	for name := range properties {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if !propertyNamePattern.MatchString(name) {
			return errBadRequest("%q is not a server.properties name.", name)
		}
		if strings.ContainsAny(properties[name], "\r\n") {
			return errBadRequest("The value of %s must be a single line.", name)
		}
	}
	return nil
}

func (s *Server) listServers(w http.ResponseWriter, r *http.Request, u *store.User) error {
	a, err := s.org(r, u, store.RoleViewer)
	if err != nil {
		return err
	}
	var list avsysv1.MinecraftServerList
	if err := s.Client.List(r.Context(), &list, client.InNamespace(a.Namespace)); err != nil {
		return err
	}
	sort.Slice(list.Items, func(i, j int) bool { return list.Items[i].Name < list.Items[j].Name })
	out := make([]serverView, len(list.Items))
	names := make([]string, len(list.Items))
	for i := range list.Items {
		out[i] = s.view(&list.Items[i])
		names[i] = list.Items[i].Name
	}
	// A list of servers is followed by their pictures: they are read now.
	s.serverIcons.mu.Lock()
	s.serverIcons.init()
	s.serverIcons.mu.Unlock()
	go s.warmIcons(a.Namespace, names)
	writeJSON(w, http.StatusOK, out)
	return nil
}

// server loads the server named in the path after checking the user's role.
func (s *Server) server(r *http.Request, u *store.User, min store.Role) (*access, *avsysv1.MinecraftServer, error) {
	a, err := s.org(r, u, min)
	if err != nil {
		return nil, nil, err
	}
	mc := &avsysv1.MinecraftServer{}
	if err := s.Client.Get(r.Context(), client.ObjectKey{Namespace: a.Namespace, Name: r.PathValue("server")}, mc); err != nil {
		return nil, nil, err
	}
	return a, mc, nil
}

func (s *Server) getServer(w http.ResponseWriter, r *http.Request, u *store.User) error {
	_, mc, err := s.server(r, u, store.RoleViewer)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, s.view(mc))
	return nil
}

func (s *Server) createServer(w http.ResponseWriter, r *http.Request, u *store.User) error {
	a, err := s.org(r, u, store.RoleAdmin)
	if err != nil {
		return err
	}
	var raw struct {
		Name string
		Spec json.RawMessage
		// FromSnapshot makes the server out of a snapshot: its files, and the
		// software and game settings that go with them.
		FromSnapshot string `json:"fromSnapshot"`
		// FromServer makes the server a copy of another in the organization,
		// as that one is now.
		FromServer string `json:"fromServer"`
		// Node is the node to put it on, rather than the one the panel would
		// choose. Its world lives on that node's disk.
		Node string `json:"node"`
		// Picture is, for one made from a snapshot or another server, a
		// picture of its own in place of the one its files bring: base64,
		// or null for none.
		Picture json.RawMessage `json:"picture"`
	}
	if err := decode(r, &raw); err != nil {
		return err
	}
	in := struct {
		Name string
		Spec avsysv1.MinecraftServerSpec
	}{Name: raw.Name}
	if len(raw.Spec) > 0 {
		if err := json.Unmarshal(raw.Spec, &in.Spec); err != nil {
			return errBadRequest("Invalid spec: %v", err)
		}
	}
	if !serverNamePattern.MatchString(in.Name) {
		return errBadRequest("Server names are up to 40 characters: lowercase letters, digits and dashes, starting with a letter.")
	}
	// An uploaded pack is the server's own, and comes by uploading it or with
	// the files of the one it is copied from, which a request leaves to them.
	if in.Spec.ResourcePack != nil && in.Spec.ResourcePack.Uploaded != nil {
		return errBadRequest("A new server is given an uploaded resource pack with PUT /orgs/{org}/servers/{server}/resource-pack once it is made, or takes the one of the server it is copied from.")
	}
	// One given here is checked. A copy's is checked once it is known what
	// its original has: the original's own is taken as it is.
	if raw.FromSnapshot == "" && raw.FromServer == "" {
		if err := checkPackSHA1(in.Spec.ResourcePack, nil); err != nil {
			return err
		}
	}
	if raw.FromSnapshot == "" && raw.FromServer == "" {
		if err := checkPackFormats(in.Spec.ResourcePack, nil); err != nil {
			return err
		}
	}
	if raw.FromSnapshot != "" && raw.FromServer != "" {
		return errBadRequest("A server is made from a snapshot or from another server, not both.")
	}
	if raw.FromSnapshot != "" || raw.FromServer != "" {
		// Which settings the request names at all, as opposed to leaving
		// out: "sleep off" and no word about sleep decode to the same thing.
		given := map[string]json.RawMessage{}
		_ = json.Unmarshal(raw.Spec, &given)
		picture, err := pictureOf(raw.Picture)
		if err != nil {
			return err
		}
		if raw.FromServer != "" {
			return s.createFromServer(w, r, u, a, in.Name, in.Spec, given, raw.FromServer, picture, raw.Node)
		}
		return s.createFromSnapshot(w, r, u, a, in.Name, in.Spec, given, raw.FromSnapshot, picture, raw.Node)
	}
	if len(raw.Picture) > 0 {
		return errBadRequest("A new server is given its picture with PUT /orgs/{org}/servers/{server}/icon?wait=true once it is made.")
	}
	s.applyDefaults(a.Org.Slug, in.Name, &in.Spec)
	// A new server has Aikar's flags unless asked not to. Only here: one
	// made before they were offered keeps starting as it always has.
	if in.Spec.JVM.AikarFlags == nil {
		on := true
		in.Spec.JVM.AikarFlags = &on
	}
	// "Latest" is settled once, here. A server that kept following it would
	// upgrade its world, and outgrow its mods, on some later restart.
	// It is the newest the chosen software has a build for, which can be
	// behind the newest there is.
	if in.Spec.Version == "LATEST" && in.Spec.Modpack == nil {
		if latest := s.latestRelease(r.Context(), in.Spec.Loader); latest != "" {
			in.Spec.Version = latest
		}
	}
	if err := s.validateSpec(r.Context(), a.Namespace, in.Name, &in.Spec); err != nil {
		return err
	}
	if err := s.checkVersion(r.Context(), &in.Spec); err != nil {
		return err
	}
	s.resolveJava(r.Context(), &in.Spec)
	if err := s.resolveContent(r.Context(), &in.Spec, nil); err != nil {
		return err
	}
	if err := s.nameIsFree(r.Context(), a, in.Name); err != nil {
		return err
	}
	if err := s.checkHostnameOwner(r.Context(), a.Org, in.Spec.Expose.Hostname); err != nil {
		return err
	}
	if err := s.ensureNamespace(r.Context(), a.Org.Slug); err != nil {
		return err
	}
	mc := &avsysv1.MinecraftServer{
		ObjectMeta: metav1.ObjectMeta{Namespace: a.Namespace, Name: in.Name},
		Spec:       in.Spec,
	}
	if raw.Node != "" {
		// Placed where asked, which the controller then keeps to.
		mc.Annotations = map[string]string{avsysv1.AnnotationNode: raw.Node}
		mc.Spec.NodeSelector = nil
	}
	if err := s.withHostname(r.Context(), a.Org, mc.Spec.Expose.Hostname, func() error { return s.createPlaced(r.Context(), mc, raw.Node) }); err != nil {
		return err
	}
	s.audit(r.Context(), u, a.Org, "server.create", in.Name, "")
	writeJSON(w, http.StatusCreated, s.view(mc))
	return nil
}

// serviceNameIsFree says whether a new server can have its Service by this
// name: not when another server's Service has it, as the one its extra
// ports are opened on, which is named after that server.
func (s *Server) serviceNameIsFree(ctx context.Context, namespace, name string) error {
	// The name is that server's from when it is made, before its Service is.
	if base, ok := strings.CutSuffix(name, "-ports"); ok {
		if err := s.Client.Get(ctx, client.ObjectKey{Namespace: namespace, Name: base}, &avsysv1.MinecraftServer{}); err == nil {
			return &Error{Status: http.StatusConflict, Code: "conflict", Message: fmt.Sprintf("%s is the name %s's extra ports are opened under. Choose another.", name, base)}
		} else if !apierrors.IsNotFound(err) {
			return err
		}
	}
	svc := &corev1.Service{}
	err := s.nodeReader().Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, svc)
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if owner := metav1.GetControllerOf(svc); owner != nil && owner.Kind == "MinecraftServer" && owner.Name != name {
		return &Error{Status: http.StatusConflict, Code: "conflict", Message: fmt.Sprintf("%s is the name %s's extra ports are opened under. Choose another.", name, owner.Name)}
	}
	return nil
}

// nameIsFree says whether a new server, other than one brought back, may
// take this name. One that a server has now is taken. So is one that a
// deleted server's snapshots are under: snapshots are kept by server name,
// and a new server of that name would have that history for its own,
// restore it, and thin its scheduled snapshots by its own policy. That name
// is for bringing that server back, until its snapshots are deleted.
//
// When the snapshots cannot be listed, the name is not refused for it: a
// fault in backups does not stop servers being made.
func (s *Server) nameIsFree(ctx context.Context, a *access, name string) error {
	if err := s.Client.Get(ctx, client.ObjectKey{Namespace: a.Namespace, Name: name}, &avsysv1.MinecraftServer{}); err == nil {
		return &Error{Status: http.StatusConflict, Code: "conflict", Message: fmt.Sprintf("There is already a server named %s.", name)}
	} else if !apierrors.IsNotFound(err) {
		return err
	}
	if err := s.serviceNameIsFree(ctx, a.Namespace, name); err != nil {
		return err
	}
	if !s.Backups.Available() {
		return nil
	}
	list, err := s.Backups.List(ctx, a.Org.Slug)
	if err != nil {
		s.Log.Warn("could not list snapshots to see whether a name has history", "org", a.Org.Slug, "name", name, "error", err)
		return nil
	}
	for _, snap := range list {
		if snap.Server == name {
			return errNameHasHistory(name)
		}
	}
	return nil
}

func errNameHasHistory(name string) error {
	return &Error{Status: http.StatusConflict, Code: "name_has_history", Message: fmt.Sprintf("There are snapshots of an earlier server named %s, and the name goes with them. Bring that server back from one of them, or give this one another name. Deleting all of them frees the name.", name)}
}

// updateServer applies a JSON merge patch to the server's spec: fields left
// out keep their value, null clears a field, and lists are replaced whole.
func (s *Server) updateServer(w http.ResponseWriter, r *http.Request, u *store.User) error {
	a, mc, err := s.server(r, u, store.RoleAdmin)
	if err != nil {
		return err
	}
	release, err := s.changing(mc, false)
	if err != nil {
		return err
	}
	defer release()
	var in struct{ Spec json.RawMessage }
	if err := decode(r, &in); err != nil {
		return err
	}
	if len(in.Spec) == 0 {
		return errBadRequest("Nothing to change: send {\"spec\": {...}}.")
	}
	var previous *avsysv1.MinecraftServerSpec
	var unlock, dropPack func()
	defer func() {
		if unlock != nil {
			unlock()
		}
	}()
	// Merged into the server as it is now, not as the cache last saw it,
	// which can lag a change made a moment ago: by this panel, or the
	// controller saying how the server is doing. Changed again between that
	// read and the write, it is read and merged into again: a merge patch
	// only changes what it names.
	for attempt := 1; ; attempt++ {
		if mc, err = s.live(r.Context(), mc); err != nil {
			return err
		}
		if previous, err = s.mergeSpec(r.Context(), a, mc, in.Spec); err != nil {
			return err
		}
		// A pack uploaded before, given another address now, has its file
		// removed, which a snapshot under way may be copying: not while one is.
		if was := previous.ResourcePack; dropPack == nil && was != nil && was.Uploaded != nil && (mc.Spec.ResourcePack == nil || mc.Spec.ResourcePack.Uploaded == nil) {
			if dropPack, err = s.changing(mc, true); err != nil {
				return err
			}
			defer dropPack()
		}
		// The server's content is held still from here until what was taken
		// off it is removed.
		if unlock == nil {
			unlock = s.lockContent(client.ObjectKeyFromObject(mc))
		}
		update := func() error { return s.Client.Update(r.Context(), mc) }
		// A hostname it has already is left alone, even under a domain claimed
		// by another since; only a new one is held to who claims what. It is
		// compared as the router reads it: a server made outside the panel may
		// have it in another case, or with a final dot.
		if mc.Spec.Expose.Hostname != router.NormalizeHostname(previous.Expose.Hostname) {
			err = s.withHostname(r.Context(), a.Org, mc.Spec.Expose.Hostname, update)
		} else {
			err = update()
		}
		if !apierrors.IsConflict(err) || attempt == 3 {
			break
		}
	}
	if apierrors.IsConflict(err) {
		return &Error{Status: http.StatusConflict, Code: "stale", Message: "This server was changed by someone else just now. Try again."}
	} else if err != nil {
		return err
	}
	s.removeDroppedUploads(r.Context(), a.Namespace, mc.Name, &mc.Spec, previous)
	s.removeDroppedPack(r.Context(), a.Namespace, mc.Name, &mc.Spec, previous)
	s.audit(r.Context(), u, a.Org, "server.update", mc.Name, "")
	// From which role members are operators in game, acted on now and not
	// with the next look.
	if previous.AutoOp != mc.Spec.AutoOp || previous.AutoWhitelist != mc.Spec.AutoWhitelist ||
		!equality.Semantic.DeepEqual(previous.OpMembers, mc.Spec.OpMembers) || !equality.Semantic.DeepEqual(previous.WhitelistMembers, mc.Spec.WhitelistMembers) {
		s.syncOpsFor(a.Org, mc)
	}
	writeJSON(w, http.StatusOK, s.view(mc))
	return nil
}

// mergeSpec applies a merge patch to a server's spec, checks the result and
// fills in what follows from it, and puts it on mc. It returns the spec as
// it was.
func (s *Server) mergeSpec(ctx context.Context, a *access, mc *avsysv1.MinecraftServer, patch json.RawMessage) (*avsysv1.MinecraftServerSpec, error) {
	current, err := json.Marshal(mc.Spec)
	if err != nil {
		return nil, err
	}
	merged, err := jsonpatch.MergePatch(current, patch)
	if err != nil {
		return nil, errBadRequest("Malformed spec patch.")
	}
	var spec avsysv1.MinecraftServerSpec
	if err := json.Unmarshal(merged, &spec); err != nil {
		return nil, errBadRequest("Invalid spec: %v", err)
	}
	s.applyDefaults(a.Org.Slug, mc.Name, &spec)
	if err := s.validateSpec(ctx, a.Namespace, mc.Name, &spec); err != nil {
		return nil, err
	}
	// Only a change of software is checked. A server whose version has since
	// left its software's list can still have everything else changed.
	if spec.Loader != mc.Spec.Loader || spec.Version != mc.Spec.Version || spec.LoaderVersion != mc.Spec.LoaderVersion {
		if err := s.checkVersion(ctx, &spec); err != nil {
			return nil, err
		}
	}
	// What Java the version needs is the panel's to say, not the request's.
	if spec.Version != mc.Spec.Version || spec.GameJava != mc.Spec.GameJava {
		s.resolveJava(ctx, &spec)
	}
	// Setting the state to Running here is starting the server by another
	// route, and is held to the same rules as the start action.
	if spec.State == avsysv1.StateRunning && mc.Spec.State != avsysv1.StateRunning {
		if err := unfinished(mc); err != nil {
			return nil, err
		}
		if s.ops.current(a.Namespace, mc.Name) != nil {
			return nil, errSnapshotting
		}
	}
	previous := mc.Spec.DeepCopy()
	if err := keepUploadedPack(&spec, previous); err != nil {
		return nil, err
	}
	if err := checkPackSHA1(spec.ResourcePack, previous.ResourcePack); err != nil {
		return nil, err
	}
	if err := checkPackFormats(spec.ResourcePack, previous.ResourcePack); err != nil {
		return nil, err
	}
	if err := s.resolveContent(ctx, &spec, previous); err != nil {
		return nil, err
	}
	mc.Spec = spec
	return previous, nil
}

func (s *Server) deleteServer(w http.ResponseWriter, r *http.Request, u *store.User) error {
	a, mc, err := s.server(r, u, store.RoleAdmin)
	if err != nil {
		return err
	}
	// A server that is still being made from a snapshot has nothing in it to
	// lose, so deleting it calls that off instead of waiting for it. A
	// restore is different: it is let finish.
	if s.ops.callOff(a.Namespace, mc.Name, kindCreate) {
		for wait := 0; s.ops.current(a.Namespace, mc.Name) != nil && wait < 300; wait++ {
			select {
			case <-r.Context().Done():
				return r.Context().Err()
			case <-time.After(100 * time.Millisecond):
			}
		}
	}
	// Without a last snapshot this is a plain delete.
	if r.URL.Query().Get("snapshot") == "false" || !s.Backups.Available() {
		// A server whose move did not finish can still be deleted.
		release, err := s.admit(mc, false)
		if err != nil {
			return err
		}
		defer release()
		if err := s.Client.Delete(r.Context(), mc); err != nil {
			return err
		}
		s.audit(r.Context(), u, a.Org, "server.delete", mc.Name, "")
		s.serverIcons.forget(a.Namespace + "/" + mc.Name)
		w.WriteHeader(http.StatusNoContent)
		return nil
	}

	// What is deleted can be brought back from its snapshots, so one is
	// taken of the server as it is now. The server is claimed for the whole
	// of it, from before the snapshot until the delete has been issued:
	// nothing else may change its files in between, because what changed
	// after the copy would be gone for good.
	op, ok := s.ops.begin(a.Namespace, a.Org.Slug, mc.Name, kindDeleteServer, "")
	if !ok {
		if running := s.ops.current(a.Namespace, mc.Name); running != nil {
			return heldBy(running.Kind)
		}
		return errBusy
	}
	// The caller going away does not leave this half done.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), operationTimeout)
	defer cancel()
	fail := func(err error) error {
		// Says so on the server, and gives the claim up.
		s.finishOperation(ctx, a.Namespace, mc.Name, kindDeleteServer, nil, nil, err)
		return err
	}
	// As it is now that the claim is held: a restore may have finished since
	// the request first looked.
	if mc, err = s.fresh(ctx, mc); err != nil {
		s.ops.end(a.Namespace, mc.Name)
		return err
	}
	detail := ""
	// A server that never got its files, or whose files a restore left in
	// between two states, has no state worth keeping.
	if unfinished(mc) == nil {
		snap, stopped, err := s.finalSnapshot(ctx, a.Org.Slug, mc, op, u.Username)
		if err != nil {
			state := "It was left as it was."
			if stopped {
				state = "It is stopped now."
			}
			return fail(&Error{Status: http.StatusBadGateway, Code: "snapshot_failed",
				Message: fmt.Sprintf("%s was not deleted, because a final snapshot of it could not be taken: %s. %s To delete it without one, say so when deleting.", mc.Name, operationFailed(err).Error(), state)})
		}
		if snap != nil {
			detail = "final snapshot " + snap.ID
			s.audit(ctx, u, a.Org, "snapshot.create", mc.Name, snap.ID)
		}
	}
	if err := s.Client.Delete(ctx, mc); err != nil {
		return fail(err)
	}
	s.ops.end(a.Namespace, mc.Name)
	s.Events.Publish(Event{Type: "operation.done", Namespace: a.Namespace, Data: operationDone{Kind: "delete", Org: a.Org.Slug, Server: mc.Name}})
	s.audit(ctx, u, a.Org, "server.delete", mc.Name, detail)
	// A server made under this name later is another server.
	s.serverIcons.forget(a.Namespace + "/" + mc.Name)
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// finalSnapshot stops a server and takes a snapshot of it, for the caller
// that is about to delete it and holds the claim on it. The snapshot is nil,
// with no error, when the server's newest one already has everything.
// stopped says whether the server got as far as being stopped.
func (s *Server) finalSnapshot(ctx context.Context, org string, mc *avsysv1.MinecraftServer, op *operation, by string) (snap *backup.Snapshot, stopped bool, err error) {
	key := client.ObjectKeyFromObject(mc)
	// Stopped first, whatever it was doing: a game that is shutting down
	// writes its world out, and one that is not running cannot change the
	// files while they are copied.
	s.setOperation(mc.Namespace, op, "Stopping the server", 0)
	if err := s.patchSpec(ctx, key, func(spec *avsysv1.MinecraftServerSpec) { spec.State = avsysv1.StateStopped }); err != nil {
		return nil, false, err
	}
	if err := s.waitPhase(ctx, key, avsysv1.PhaseStopped, stopWait); err != nil {
		return nil, false, fmt.Errorf("the server did not stop: %w", err)
	}
	snap, err = s.takeSnapshot(ctx, org, mc, op, backup.TriggerFinal, by)
	if errors.Is(err, backup.ErrUnchanged) {
		return nil, true, nil
	}
	return snap, true, err
}

func (s *Server) serverAction(w http.ResponseWriter, r *http.Request, u *store.User) error {
	a, mc, err := s.server(r, u, store.RoleOperator)
	if err != nil {
		return err
	}
	action := r.PathValue("action")
	// Starting or restarting the game under a snapshot would spoil the copy,
	// so those wait for it. Stopping is always allowed outside a restore.
	starts := action == "start" || action == "restart"
	release, err := s.changing(mc, starts)
	if err != nil {
		return err
	}
	defer release()
	if starts {
		if err := unfinished(mc); err != nil {
			return err
		}
	}
	orig := mc.DeepCopy()
	switch action {
	case "start":
		mc.Spec.State = avsysv1.StateRunning
		delete(mc.Annotations, avsysv1.AnnotationAsleep)
	case "stop":
		mc.Spec.State = avsysv1.StateStopped
		delete(mc.Annotations, avsysv1.AnnotationAsleep)
	case "restart":
		// With nothing new to apply, asking the game to stop is enough: its
		// container restarts in place and open shells and editors survive.
		// Otherwise the pod is replaced so the new spec takes effect.
		live, err := s.live(r.Context(), mc)
		if err != nil {
			return err
		}
		if restartsInPlace(live) {
			if _, err := s.rcon(r.Context(), mc, "stop"); err == nil {
				break
			}
		}
		if mc.Annotations == nil {
			mc.Annotations = map[string]string{}
		}
		mc.Annotations[avsysv1.AnnotationRestart] = time.Now().UTC().Format(time.RFC3339Nano)
		delete(mc.Annotations, avsysv1.AnnotationAsleep)
	default:
		return errNotFound
	}
	if err := s.Client.Patch(r.Context(), mc, client.MergeFrom(orig)); err != nil {
		return err
	}
	s.audit(r.Context(), u, a.Org, "server."+action, mc.Name, "")
	writeJSON(w, http.StatusOK, s.view(mc))
	return nil
}

// restartsInPlace says whether a restart can be the game stopping and its
// container starting again in the same pod, with the settings it has. Only
// when the controller has seen the server's latest settings and found
// nothing new in them: a change made a moment ago, such as content taken
// off, would otherwise be started without, and the old settings put back
// what was removed.
func restartsInPlace(mc *avsysv1.MinecraftServer) bool {
	return mc.Status.Phase == avsysv1.PhaseRunning && !mc.Status.PendingRestart && mc.Status.ObservedGeneration == mc.Generation
}

// live reads a server as it is now, where the cache may lag a change just
// made.
func (s *Server) live(ctx context.Context, mc *avsysv1.MinecraftServer) (*avsysv1.MinecraftServer, error) {
	// Read again through the client where there is no reader of its own:
	// mc may be one a change was already merged into, and be stale.
	reader := s.Reader
	if reader == nil {
		reader = s.Client
	}
	now := &avsysv1.MinecraftServer{}
	if err := reader.Get(ctx, client.ObjectKeyFromObject(mc), now); err != nil {
		return nil, err
	}
	return now, nil
}

var sha1Pattern = regexp.MustCompile(`^[0-9a-f]{40}$`)

// checkPackSHA1 checks a resource pack's SHA-1, which the game checks the
// pack it downloads against: a wrong one would have every player refuse it.
// One the server had before (was) is left as it is, as one typed before
// this was checked: it does not hold up changes to anything else.
func checkPackSHA1(rp, was *avsysv1.ResourcePackSpec) error {
	if rp == nil || rp.SHA1 == "" || (was != nil && rp.SHA1 == was.SHA1) {
		return nil
	}
	rp.SHA1 = strings.ToLower(strings.TrimSpace(rp.SHA1))
	if !sha1Pattern.MatchString(rp.SHA1) {
		return errBadRequest("A resource pack's SHA-1 is 40 hexadecimal digits.")
	}
	return nil
}

// keepUploadedPack holds a change of settings to the pack uploaded to the
// server: one can be kept, as it is, but not made up. A pack given another
// address is one hosted elsewhere now, and no longer the uploaded one.
func keepUploadedPack(spec, previous *avsysv1.MinecraftServerSpec) error {
	rp, was := spec.ResourcePack, previous.ResourcePack
	// A pack picked from a project is that project's file: given another
	// address, and not another project or version with it, it is no
	// longer the project's.
	if rp != nil && rp.Project != nil && was != nil && was.Project != nil && *rp.Project == *was.Project && rp.URL != was.URL {
		rp.Project = nil
	}
	// Saying none is not how an uploaded pack is removed, which takes its
	// file too: it is what settings from before it was uploaded say.
	if (rp == nil || rp.URL == "") && was != nil && was.Uploaded != nil {
		spec.ResourcePack = was.DeepCopy()
		// Checked as the version it is kept with, which may be another.
		return packFits(spec)
	}
	if rp == nil || rp.Uploaded == nil {
		return nil
	}
	if was == nil || was.Uploaded == nil {
		return errBadRequest("A resource pack is uploaded with PUT /orgs/{org}/servers/{server}/resource-pack.")
	}
	if rp.URL != was.URL {
		rp.Uploaded = nil
		return nil
	}
	if rp.SHA1 != was.SHA1 {
		return errBadRequest("An uploaded resource pack's SHA-1 is that of the file. Upload another to change it.")
	}
	rp.Uploaded = was.Uploaded.DeepCopy()
	// Its formats are what its file says.
	rp.Formats = was.Formats.DeepCopy()
	// The panel's own file is no project's.
	rp.Project = nil
	return nil
}

// checkPorts checks a server's extra ports beyond what the CRD says: each
// name and each number and protocol once, and not the game's own.
func checkPorts(ports []avsysv1.ExtraPort) error {
	names, numbers := map[string]bool{}, map[string]bool{}
	for i := range ports {
		p := &ports[i]
		if p.Protocol == "" {
			p.Protocol = corev1.ProtocolTCP
		}
		if len(p.Name) > 15 || !portName.MatchString(p.Name) {
			return errBadRequest("A port's name is up to 15 lowercase letters, digits and dashes, starting with a letter and ending with a letter or digit, with no two dashes together, as voice-chat.")
		}
		if p.Name == "minecraft" {
			return errBadRequest("minecraft is the game port's name; call it something else.")
		}
		if p.Port < 1 || p.Port > 65535 {
			return errBadRequest("%d is not a port.", p.Port)
		}
		if p.Port == 25565 {
			return errBadRequest("25565 is the game's own port, not an extra one.")
		}
		if p.Port == 25575 {
			return errBadRequest("25575 is the game's console, and is not opened.")
		}
		if p.Protocol != corev1.ProtocolTCP && p.Protocol != corev1.ProtocolUDP {
			return errBadRequest("A port is TCP or UDP.")
		}
		key := fmt.Sprintf("%d/%s", p.Port, p.Protocol)
		if names[p.Name] || numbers[key] {
			return errBadRequest("Each port is there once: %s (%s) appears twice.", p.Name, key)
		}
		names[p.Name], numbers[key] = true, true
	}
	if len(ports) > 8 {
		return errBadRequest("A server has up to 8 extra ports.")
	}
	return nil
}

// portName is a Service port's name, which a port's is: a letter first, a
// letter or digit last, no two dashes together.
var portName = regexp.MustCompile(`^[a-z](-?[a-z0-9])*$`)

// rcon runs a console command on a running server.
func (s *Server) rcon(ctx context.Context, mc *avsysv1.MinecraftServer, command string) (string, error) {
	return s.rconFor(ctx, mc, command, 15*time.Second)
}

// rconFor is rcon for commands that may take longer than most to answer.
func (s *Server) rconFor(ctx context.Context, mc *avsysv1.MinecraftServer, command string, timeout time.Duration) (string, error) {
	// Read straight from the API server: caching secrets cluster-wide just
	// for this would be a lot of privilege and memory.
	secret, err := s.Kube.CoreV1().Secrets(mc.Namespace).Get(ctx, controller.RCONSecretName(mc.Name), metav1.GetOptions{})
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	conn, err := s.Dialer.DialPod(ctx, mc.Namespace, mc.Name, controller.RCONPort)
	if err != nil {
		return "", err
	}
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	defer conn.Close()
	return rcon.Run(conn, string(secret.Data["password"]), command)
}
