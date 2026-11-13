package api

import (
	"context"
	"errors"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/client"

	avsysv1 "github.com/GT-Drehmal/avSYS/api/v1alpha1"
	"github.com/GT-Drehmal/avSYS/internal/controller"
)

// PinLatest keeps each server saved on "Latest" before servers were kept on
// one version on the version it has, once: the one its game last said it
// runs, or for one that has never run, the newest its software has a build
// for now. Until then the image would move it to a new game version on
// some restart, by itself. A server it cannot tell the version of is left
// as it is, and the log says why. One it could not settle for want of the
// list of versions is tried again later.
func (s *Server) PinLatest(ctx context.Context) {
	for wait := time.Minute; ; wait = min(2*wait, time.Hour) {
		if !s.pinLatest(ctx) {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
	}
}

// errPinLater is for a server that could not be settled now, but can be.
var errPinLater = errors.New("try again later")

// pinLatest settles the servers on "Latest" there are now, and says whether
// any is to be tried again.
func (s *Server) pinLatest(ctx context.Context) (again bool) {
	var all avsysv1.MinecraftServerList
	if err := s.Client.List(ctx, &all); err != nil {
		s.Log.Error("looking for servers on Latest to keep on their version", "error", err)
		return true
	}
	for i := range all.Items {
		mc := &all.Items[i]
		if mc.Spec.Version != "LATEST" || mc.Spec.Modpack != nil {
			continue
		}
		version, how, err := s.versionToPin(ctx, mc)
		if errors.Is(err, errPinLater) {
			s.Log.Warn("a server on Latest is not kept on a version yet, and is tried again later", "server", mc.Name, "namespace", mc.Namespace, "why", how)
			again = true
			continue
		}
		if err != nil {
			s.Log.Warn("a server on Latest is left on it", "server", mc.Name, "namespace", mc.Namespace, "why", how)
			continue
		}
		orig := mc.DeepCopy()
		mc.Spec.Version = version
		s.resolveJava(ctx, &mc.Spec)
		// Only if nobody changed it meanwhile: whatever they set is theirs.
		if err := s.Client.Patch(ctx, mc, client.MergeFromWithOptions(orig, client.MergeFromWithOptimisticLock{})); err != nil {
			s.Log.Warn("a server on Latest could not be kept on its version, and is tried again later", "server", mc.Name, "namespace", mc.Namespace, "version", version, "error", err)
			again = true
			continue
		}
		s.Log.Info("kept a server on Latest on its version", "server", mc.Name, "namespace", mc.Namespace, "version", version, "from", how)
	}
	return again
}

// versionToPin is the version a server on "Latest" is kept on, and how that
// was found, or why it was not.
func (s *Server) versionToPin(ctx context.Context, mc *avsysv1.MinecraftServer) (version, how string, err error) {
	ran := controller.RanVersion(mc.Status.Version)
	if ran == "" {
		// Its game has never said what it runs. One that has never been
		// up gets the newest, as a server made now does.
		if mc.Status.LastActive != nil {
			return "", "it has run, but never said which version", errors.New("unknown")
		}
		latest := s.latestRelease(ctx, mc.Spec.Loader)
		if latest == "" {
			return "", "it has never run, and the newest version could not be found", errPinLater
		}
		return latest, "the newest, as it has never run", nil
	}
	m, err := s.manifest(ctx)
	if err != nil {
		return "", "the list of game versions could not be read", errPinLater
	}
	listed := false
	for _, v := range m.Versions {
		listed = listed || v.ID == ran
	}
	if !listed {
		return "", "the version it runs could not be told from " + mc.Status.Version, errors.New("unknown")
	}
	// Its software may have been changed since, to one without a build of it.
	spec := mc.Spec
	spec.Version = ran
	if err := s.checkVersion(ctx, &spec); err != nil {
		return "", "it runs " + ran + ", which its software now has no build for", err
	}
	return ran, "the version it runs (" + mc.Status.Version + ")", nil
}
