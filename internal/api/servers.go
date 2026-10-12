package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"

	jsonpatch "gopkg.in/evanphx/json-patch.v4"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	avsysv1 "github.com/GT-Drehmal/avSYS/api/v1alpha1"
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
	for i := range list.Items {
		out[i] = s.view(&list.Items[i])
	}
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
	var in struct {
		Name string
		Spec avsysv1.MinecraftServerSpec
	}
	if err := decode(r, &in); err != nil {
		return err
	}
	if !serverNamePattern.MatchString(in.Name) {
		return errBadRequest("Server names are up to 40 characters: lowercase letters, digits and dashes, starting with a letter.")
	}
	s.applyDefaults(a.Org.Slug, in.Name, &in.Spec)
	// "Latest" is settled once, here. A server that kept following it would
	// upgrade its world, and outgrow its mods, on some later restart.
	if in.Spec.Version == "LATEST" && in.Spec.Modpack == nil {
		if latest := s.latestRelease(r.Context()); latest != "" {
			in.Spec.Version = latest
		}
	}
	if err := s.validateSpec(r.Context(), a.Namespace, in.Name, &in.Spec); err != nil {
		return err
	}
	if err := s.resolveContent(r.Context(), &in.Spec, nil); err != nil {
		return err
	}
	if err := s.ensureNamespace(r.Context(), a.Org.Slug); err != nil {
		return err
	}
	mc := &avsysv1.MinecraftServer{
		ObjectMeta: metav1.ObjectMeta{Namespace: a.Namespace, Name: in.Name},
		Spec:       in.Spec,
	}
	if err := s.Client.Create(r.Context(), mc); err != nil {
		return err
	}
	s.audit(r.Context(), u, a.Org, "server.create", in.Name, "")
	writeJSON(w, http.StatusCreated, s.view(mc))
	return nil
}

// updateServer applies a JSON merge patch to the server's spec: fields left
// out keep their value, null clears a field, and lists are replaced whole.
func (s *Server) updateServer(w http.ResponseWriter, r *http.Request, u *store.User) error {
	a, mc, err := s.server(r, u, store.RoleAdmin)
	if err != nil {
		return err
	}
	if err := s.notRestoring(mc); err != nil {
		return err
	}
	var in struct{ Spec json.RawMessage }
	if err := decode(r, &in); err != nil {
		return err
	}
	if len(in.Spec) == 0 {
		return errBadRequest("Nothing to change: send {\"spec\": {...}}.")
	}
	current, err := json.Marshal(mc.Spec)
	if err != nil {
		return err
	}
	merged, err := jsonpatch.MergePatch(current, in.Spec)
	if err != nil {
		return errBadRequest("Malformed spec patch.")
	}
	var spec avsysv1.MinecraftServerSpec
	if err := json.Unmarshal(merged, &spec); err != nil {
		return errBadRequest("Invalid spec: %v", err)
	}
	s.applyDefaults(a.Org.Slug, mc.Name, &spec)
	if err := s.validateSpec(r.Context(), a.Namespace, mc.Name, &spec); err != nil {
		return err
	}
	previous := mc.Spec.DeepCopy()
	if err := s.resolveContent(r.Context(), &spec, previous); err != nil {
		return err
	}
	mc.Spec = spec
	// Update carries the resource version read above, so a concurrent edit
	// is rejected instead of silently overwritten.
	if err := s.Client.Update(r.Context(), mc); apierrors.IsConflict(err) {
		return &Error{Status: http.StatusConflict, Code: "stale", Message: "This server was changed by someone else just now. Try again."}
	} else if err != nil {
		return err
	}
	s.removeDroppedUploads(r.Context(), a.Namespace, mc.Name, previous.Mods, mc.Spec.Mods)
	s.audit(r.Context(), u, a.Org, "server.update", mc.Name, "")
	writeJSON(w, http.StatusOK, s.view(mc))
	return nil
}

func (s *Server) deleteServer(w http.ResponseWriter, r *http.Request, u *store.User) error {
	a, mc, err := s.server(r, u, store.RoleAdmin)
	if err != nil {
		return err
	}
	if err := s.notRestoring(mc); err != nil {
		return err
	}
	if err := s.Client.Delete(r.Context(), mc); err != nil {
		return err
	}
	s.audit(r.Context(), u, a.Org, "server.delete", mc.Name, "")
	w.WriteHeader(http.StatusNoContent)
	return nil
}

func (s *Server) serverAction(w http.ResponseWriter, r *http.Request, u *store.User) error {
	a, mc, err := s.server(r, u, store.RoleOperator)
	if err != nil {
		return err
	}
	if err := s.notRestoring(mc); err != nil {
		return err
	}
	action := r.PathValue("action")
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
		if mc.Status.Phase == avsysv1.PhaseRunning && !mc.Status.PendingRestart {
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
