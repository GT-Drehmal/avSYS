package api

// The panel moving itself to a newer release: the instance administrator
// presses Update, or the panel is set to take fix releases by itself, and it
// changes the image of its own Deployment. The cluster then starts the new
// version in its place. Nothing outside the cluster is given a way in: the
// panel pulls, through its own service account, which may read and change
// that one Deployment and nothing else of its kind.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	avsysv1 "github.com/GT-Drehmal/avSYS/api/v1alpha1"
	"github.com/GT-Drehmal/avSYS/internal/controller"
	"github.com/GT-Drehmal/avSYS/internal/mcproto"
	"github.com/GT-Drehmal/avSYS/internal/release"
	"github.com/GT-Drehmal/avSYS/internal/store"
)

// autoUpdateKey is the setting that has the panel take fix releases by
// itself: "true", or anything else for not.
const autoUpdateKey = "panel.auto-update"

// selfContainer is the name of the panel's container in its Deployment.
const selfContainer = "avsys"

// workspaceImageEnv names the workspace image where it is set by hand.
const workspaceImageEnv = "AVSYS_WORKSPACE_IMAGE"

var errCannotUpdate = &Error{Status: http.StatusConflict, Code: "cannot_update", Message: "avSYS cannot update itself here."}

// panelView is the panel itself, for the instance administrator.
type panelView struct {
	Version string `json:"version"`
	// CanUpdate is whether the panel can move itself to a newer release.
	CanUpdate bool `json:"canUpdate"`
	// AutoUpdate is whether it takes fix releases by itself.
	AutoUpdate bool `json:"autoUpdate"`
	// Updating is the release it is moving to, while it does.
	Updating string `json:"updating,omitempty"`
	// CurseForge is where its CurseForge API key comes from: "panel" for
	// one entered in it, "install" for one it was installed with, or ""
	// for none. The key itself is never shown.
	CurseForge string `json:"curseforge"`
}

// selfDeployment is the panel's own Deployment and the index of the panel's
// container in it, where the panel can read it.
func (s *Server) selfDeployment(ctx context.Context) (*appsv1.Deployment, int, error) {
	at := s.Config.Deployment
	if s.Kube == nil || at.Name == "" {
		return nil, 0, errCannotUpdate
	}
	d, err := s.Kube.AppsV1().Deployments(at.Namespace).Get(ctx, at.Name, metav1.GetOptions{})
	if err != nil {
		return nil, 0, err
	}
	containers := d.Spec.Template.Spec.Containers
	for i := range containers {
		if containers[i].Name == selfContainer {
			return d, i, nil
		}
	}
	if len(containers) == 1 {
		return d, 0, nil
	}
	return nil, 0, errCannotUpdate
}

func (s *Server) autoUpdate(ctx context.Context) bool {
	v, err := s.Store.Setting(ctx, autoUpdateKey)
	return err == nil && v == "true"
}

// panelOf is what the instance administrator is shown of the panel.
func (s *Server) panelOf(ctx context.Context) panelView {
	v := panelView{Version: s.Config.Version, AutoUpdate: s.autoUpdate(ctx), CurseForge: s.curseForgeKeyFrom(ctx)}
	// Only a release that looks for newer ones has somewhere to go.
	if s.Updates == nil || !release.IsRelease(s.Config.Version) {
		return v
	}
	d, i, err := s.selfDeployment(ctx)
	if err != nil {
		return v
	}
	v.CanUpdate = true
	// Told to run a newer release than this one: the new one is on its way.
	if tag := release.TagOf(d.Spec.Template.Spec.Containers[i].Image); release.Newer(s.Config.Version, tag) {
		v.Updating = tag
	}
	return v
}

// updateTo moves the panel's Deployment to the release given. The workspace
// image follows where it is set by hand to this version's: otherwise the
// new version starts its own by itself.
func (s *Server) updateTo(ctx context.Context, version string) error {
	s.selfUpdate.Lock()
	defer s.selfUpdate.Unlock()
	d, i, err := s.selfDeployment(ctx)
	if apierrors.IsForbidden(err) || apierrors.IsNotFound(err) {
		return errCannotUpdate
	} else if err != nil {
		return err
	}
	c := d.Spec.Template.Spec.Containers[i]
	if release.TagOf(c.Image) == version {
		return nil
	}
	container := map[string]any{"name": c.Name, "image": release.WithTag(c.Image, version)}
	for _, e := range c.Env {
		if e.Name == workspaceImageEnv && e.ValueFrom == nil && release.TagOf(e.Value) == s.Config.Version {
			container["env"] = []map[string]string{{"name": e.Name, "value": release.WithTag(e.Value, version)}}
		}
	}
	patch, err := json.Marshal(map[string]any{"spec": map[string]any{"template": map[string]any{"spec": map[string]any{
		"containers": []any{container},
	}}}})
	if err != nil {
		return err
	}
	_, err = s.Kube.AppsV1().Deployments(d.Namespace).Patch(ctx, d.Name, types.StrategicMergePatchType, patch, metav1.PatchOptions{FieldManager: "avsys"})
	if apierrors.IsForbidden(err) {
		return errCannotUpdate
	}
	return err
}

// updatePanel moves the panel to the newest release, which the
// administrator was shown.
func (s *Server) updatePanel(w http.ResponseWriter, r *http.Request, u *store.User) error {
	if !u.IsAdmin {
		return errForbidden
	}
	var in struct {
		Version string `json:"version"`
	}
	if err := decode(r, &in); err != nil {
		return err
	}
	latest := s.Updates.Newer()
	if latest == nil || (in.Version != "" && in.Version != latest.Version) {
		return &Error{Status: http.StatusConflict, Code: "no_update", Message: "There is no newer release to update to."}
	}
	if err := s.updateTo(r.Context(), latest.Version); err != nil {
		return err
	}
	s.audit(r.Context(), u, nil, "panel.update", latest.Version, s.Config.Version)
	s.Log.Info("updating", "from", s.Config.Version, "to", latest.Version, "by", u.Username)
	writeJSON(w, http.StatusAccepted, s.panelOf(r.Context()))
	return nil
}

// getPanel is the panel itself, for the instance administrator's settings:
// the version it runs and how it updates, a newer release where there is
// one, and where its CurseForge key comes from.
func (s *Server) getPanel(w http.ResponseWriter, r *http.Request, u *store.User) error {
	if !u.IsAdmin {
		return errForbidden
	}
	answer := map[string]any{"panel": s.panelOf(r.Context())}
	if latest := s.Updates.Newer(); latest != nil {
		answer["update"] = s.updateOf(r.Context(), latest)
	}
	writeJSON(w, http.StatusOK, answer)
	return nil
}

// setPanel sets whether the panel takes fix releases by itself, and its
// CurseForge API key: "" removes the one entered in it.
func (s *Server) setPanel(w http.ResponseWriter, r *http.Request, u *store.User) error {
	if !u.IsAdmin {
		return errForbidden
	}
	var in struct {
		AutoUpdate    *bool   `json:"autoUpdate"`
		CurseForgeKey *string `json:"curseforgeKey"`
	}
	if err := decode(r, &in); err != nil {
		return err
	}
	if in.CurseForgeKey != nil {
		if err := s.setCurseForgeKey(r.Context(), u, *in.CurseForgeKey); err != nil {
			return err
		}
		if in.AutoUpdate == nil {
			writeJSON(w, http.StatusOK, s.panelOf(r.Context()))
			return nil
		}
	}
	if in.AutoUpdate == nil {
		return errBadRequest("Say whether avSYS updates by itself.")
	}
	value := "false"
	if *in.AutoUpdate {
		value = "true"
	}
	if err := s.Store.SetSetting(r.Context(), autoUpdateKey, value); err != nil {
		return err
	}
	s.audit(r.Context(), u, nil, "panel.auto-update", value, "")
	writeJSON(w, http.StatusOK, s.panelOf(r.Context()))
	return nil
}

// playing is how many players are on servers now, as each game whose server
// is up says when asked now: what the last look found may be old. An error
// where a game that may have players does not answer, since nobody playing
// cannot then be told.
func (s *Server) playing(ctx context.Context) (int32, error) {
	var list avsysv1.MinecraftServerList
	if err := s.Client.List(ctx, &list); err != nil {
		return 0, err
	}
	var n int32
	for i := range list.Items {
		mc := &list.Items[i]
		switch mc.Status.Phase {
		case avsysv1.PhaseRunning, avsysv1.PhaseStarting, avsysv1.PhaseStopping:
		default:
			// No game up to be on.
			continue
		}
		online, err := s.pingPlayers(ctx, mc)
		if err != nil {
			return 0, fmt.Errorf("%s/%s does not say who is on it: %w", mc.Namespace, mc.Name, err)
		}
		n += online
	}
	return n, nil
}

// pingPlayers asks a server's game how many players are on it.
func (s *Server) pingPlayers(ctx context.Context, mc *avsysv1.MinecraftServer) (int32, error) {
	if s.Dialer == nil {
		return 0, errors.New("no way to reach servers")
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	conn, err := s.Dialer.DialPod(ctx, mc.Namespace, mc.Name, controller.MinecraftPort)
	if err != nil {
		return 0, err
	}
	// Tunnelled connections ignore deadlines, so the time limit closes it.
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	defer conn.Close()
	st, err := mcproto.Ping(conn, "localhost")
	if err != nil {
		return 0, err
	}
	return int32(st.Players.Online), nil
}

// AutoUpdate moves the panel to a newer fix release, if it is set to take
// them by itself and nobody is playing: the panel carries players'
// connections, which an update cuts. It says whether it did.
func (s *Server) AutoUpdate(ctx context.Context) bool {
	if !s.autoUpdate(ctx) {
		return false
	}
	latest := s.Updates.Newer()
	if latest == nil || !release.IsFix(s.Config.Version, latest.Version) {
		return false
	}
	if n, err := s.playing(ctx); err != nil || n > 0 {
		if err != nil {
			s.Log.Debug("not updating by itself yet", "error", err)
		}
		return false
	}
	if err := s.updateTo(ctx, latest.Version); err != nil {
		if !errors.Is(err, errCannotUpdate) {
			s.Log.Warn("could not update by itself", "to", latest.Version, "error", err)
		}
		return false
	}
	s.Log.Info("updating by itself", "from", s.Config.Version, "to", latest.Version)
	return true
}

// RunAutoUpdates looks every few minutes for a fix release to move to, until
// ctx is done.
func (s *Server) RunAutoUpdates(ctx context.Context) {
	if s.Updates == nil || !release.IsRelease(s.Config.Version) {
		return
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(10 * time.Minute):
		}
		s.AutoUpdate(ctx)
	}
}
