package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	avsysv1 "github.com/GT-Drehmal/avSYS/api/v1alpha1"
	"github.com/GT-Drehmal/avSYS/internal/store"
)

var slugPattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,30}[a-z0-9])?$`)

func (s *Server) createOrg(w http.ResponseWriter, r *http.Request, u *store.User) error {
	if !u.IsAdmin {
		return &Error{Status: http.StatusForbidden, Code: "forbidden", Message: "Only the panel's administrator can create organizations."}
	}
	var in struct{ Name, Slug string }
	if err := decode(r, &in); err != nil {
		return err
	}
	in.Name, in.Slug = strings.TrimSpace(in.Name), strings.TrimSpace(in.Slug)
	if in.Name == "" || len(in.Name) > 64 {
		return errBadRequest("Give the organization a name of up to 64 characters.")
	}
	if !slugPattern.MatchString(in.Slug) {
		return errBadRequest("Short names are up to 32 characters: lowercase letters, digits and dashes.")
	}
	org := &store.Org{Name: in.Name, Slug: in.Slug}
	if err := s.Store.CreateOrg(r.Context(), org, u.ID); err != nil {
		return err
	}
	if err := s.ensureNamespace(r.Context(), org.Slug); err != nil {
		// Without its namespace the organization is unusable; undo rather
		// than leave it half made.
		_ = s.Store.DeleteOrg(context.WithoutCancel(r.Context()), org.ID)
		return err
	}
	s.audit(r.Context(), u, org, "org.create", org.Slug, "")
	writeJSON(w, http.StatusCreated, store.OrgMembership{Org: *org, Role: store.RoleOwner})
	return nil
}

func (s *Server) ensureNamespace(ctx context.Context, slug string) error {
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
		Name:   s.Namespace(slug),
		Labels: map[string]string{avsysv1.LabelOrg: slug},
	}}
	if err := s.Client.Create(ctx, ns); err != nil && !apierrors.IsAlreadyExists(err) {
		return err
	}
	return nil
}

func (s *Server) getOrg(w http.ResponseWriter, r *http.Request, u *store.User) error {
	a, err := s.org(r, u, store.RoleViewer)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, store.OrgMembership{Org: *a.Org, Role: a.Role})
	return nil
}

func (s *Server) updateOrg(w http.ResponseWriter, r *http.Request, u *store.User) error {
	a, err := s.org(r, u, store.RoleOwner)
	if err != nil {
		return err
	}
	var in struct{ Name string }
	if err := decode(r, &in); err != nil {
		return err
	}
	if in.Name = strings.TrimSpace(in.Name); in.Name == "" || len(in.Name) > 64 {
		return errBadRequest("Give the organization a name of up to 64 characters.")
	}
	if err := s.Store.RenameOrg(r.Context(), a.Org.ID, in.Name); err != nil {
		return err
	}
	a.Org.Name = in.Name
	writeJSON(w, http.StatusOK, store.OrgMembership{Org: *a.Org, Role: a.Role})
	return nil
}

func (s *Server) deleteOrg(w http.ResponseWriter, r *http.Request, u *store.User) error {
	a, err := s.org(r, u, store.RoleOwner)
	if err != nil {
		return err
	}
	var servers avsysv1.MinecraftServerList
	if err := s.Client.List(r.Context(), &servers, client.InNamespace(a.Namespace)); err != nil {
		return err
	}
	if len(servers.Items) > 0 {
		return &Error{Status: http.StatusConflict, Code: "not_empty", Message: "Delete this organization's servers first."}
	}
	// Snapshots outlive servers, so an organization with none may still hold
	// years of history. Removing that has to be asked for by name. If how
	// much there is cannot be said, it is assumed to be some.
	if count, known := s.Backups.Holds(r.Context(), a.Org.Slug); (!known || count > 0) && r.URL.Query().Get("snapshots") != "delete" {
		how := "they could not be counted"
		if known {
			how = fmt.Sprint(count)
		}
		return &Error{Status: http.StatusConflict, Code: "has_snapshots",
			Message: fmt.Sprintf("This organization still has snapshots (%s). Deleting it deletes them too; confirm with ?snapshots=delete.", how)}
	}
	// The repository goes first. If it cannot be removed the organization
	// stays, name included: a later organization of the same name would
	// otherwise find itself holding this one's snapshots and their password.
	if err := s.Backups.Destroy(a.Org.Slug); err != nil {
		return fmt.Errorf("removing the organization's snapshots: %w", err)
	}
	// What was read out of them goes too: a later organization of the same
	// name starts a repository of its own.
	s.snapshotIcons.forgetOrg(a.Org.Slug)
	// Only remove a namespace avSYS created for this organization.
	ns := &corev1.Namespace{}
	err = s.Client.Get(r.Context(), client.ObjectKey{Name: a.Namespace}, ns)
	if err == nil && ns.Labels[avsysv1.LabelOrg] == a.Org.Slug {
		err = s.Client.Delete(r.Context(), ns)
	}
	if client.IgnoreNotFound(err) != nil {
		return err
	}
	if err := s.Store.DeleteOrg(r.Context(), a.Org.ID); err != nil {
		return err
	}
	_ = s.Store.DeleteSetting(context.WithoutCancel(r.Context()), policyKey(a.Org.ID))
	s.audit(r.Context(), u, nil, "org.delete", a.Org.Slug, "")
	w.WriteHeader(http.StatusNoContent)
	return nil
}

func (s *Server) listMembers(w http.ResponseWriter, r *http.Request, u *store.User) error {
	a, err := s.org(r, u, store.RoleViewer)
	if err != nil {
		return err
	}
	members, err := s.Store.Members(r.Context(), a.Org.ID)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, members)
	return nil
}

var errLastOwner = &Error{Status: http.StatusConflict, Code: "last_owner", Message: "An organization needs at least one owner."}

// guardOwnerChange enforces the rules around the owner role: only owners may
// touch another owner, and the last owner cannot be removed or demoted.
func (s *Server) guardOwnerChange(ctx context.Context, a *access, targetID string) error {
	target, err := s.Store.RoleIn(ctx, a.Org.ID, targetID)
	if err != nil {
		return err
	}
	if target != store.RoleOwner {
		return nil
	}
	if a.Role != store.RoleOwner {
		return errForbidden
	}
	owners, err := s.Store.CountOwners(ctx, a.Org.ID)
	if err != nil {
		return err
	}
	if owners <= 1 {
		return errLastOwner
	}
	return nil
}

func (s *Server) updateMember(w http.ResponseWriter, r *http.Request, u *store.User) error {
	a, err := s.org(r, u, store.RoleAdmin)
	if err != nil {
		return err
	}
	var in struct{ Role store.Role }
	if err := decode(r, &in); err != nil {
		return err
	}
	if !in.Role.Valid() {
		return errBadRequest("Unknown role.")
	}
	if in.Role == store.RoleOwner && a.Role != store.RoleOwner {
		return errForbidden
	}
	target := r.PathValue("user")
	if err := s.guardOwnerChange(r.Context(), a, target); err != nil {
		return err
	}
	if err := s.Store.SetRole(r.Context(), a.Org.ID, target, in.Role); err != nil {
		return err
	}
	s.audit(r.Context(), u, a.Org, "member.role", target, string(in.Role))
	// Who is an operator in game goes by role.
	s.syncOpsSoon(a.Org)
	w.WriteHeader(http.StatusNoContent)
	return nil
}

func (s *Server) removeMember(w http.ResponseWriter, r *http.Request, u *store.User) error {
	target := r.PathValue("user")
	// Anyone may leave; removing someone else takes an admin.
	min := store.RoleAdmin
	if target == u.ID {
		min = store.RoleViewer
	}
	a, err := s.org(r, u, min)
	if err != nil {
		return err
	}
	if err := s.guardOwnerChange(r.Context(), a, target); err != nil {
		return err
	}
	if err := s.Store.RemoveMember(r.Context(), a.Org.ID, target); err != nil {
		return err
	}
	s.audit(r.Context(), u, a.Org, "member.remove", target, "")
	s.syncOpsSoon(a.Org)
	w.WriteHeader(http.StatusNoContent)
	return nil
}

func (s *Server) listInvites(w http.ResponseWriter, r *http.Request, u *store.User) error {
	a, err := s.org(r, u, store.RoleAdmin)
	if err != nil {
		return err
	}
	invites, err := s.Store.Invites(r.Context(), a.Org.ID)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, invites)
	return nil
}

func (s *Server) createInvite(w http.ResponseWriter, r *http.Request, u *store.User) error {
	a, err := s.org(r, u, store.RoleAdmin)
	if err != nil {
		return err
	}
	var in struct {
		Role           store.Role
		ExpiresInHours int
		MaxUses        int
	}
	if err := decode(r, &in); err != nil {
		return err
	}
	if !in.Role.Valid() {
		return errBadRequest("Unknown role.")
	}
	// Nobody can hand out more than they hold.
	if !a.Role.AtLeast(in.Role) {
		return errForbidden
	}
	if in.ExpiresInHours < 0 || in.MaxUses < 0 {
		return errBadRequest("Expiry and use limits cannot be negative.")
	}
	inv := &store.Invite{OrgID: a.Org.ID, Role: in.Role, CreatedBy: u.Username, MaxUses: in.MaxUses}
	if in.ExpiresInHours > 0 {
		t := time.Now().Add(time.Duration(in.ExpiresInHours) * time.Hour).UTC()
		inv.ExpiresAt = &t
	}
	if err := s.Store.CreateInvite(r.Context(), inv); err != nil {
		return err
	}
	s.audit(r.Context(), u, a.Org, "invite.create", string(in.Role), "")
	writeJSON(w, http.StatusCreated, inv)
	return nil
}

func (s *Server) deleteInvite(w http.ResponseWriter, r *http.Request, u *store.User) error {
	a, err := s.org(r, u, store.RoleAdmin)
	if err != nil {
		return err
	}
	if err := s.Store.DeleteInvite(r.Context(), a.Org.ID, r.PathValue("token")); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// previewInvite tells someone holding an invite link what it is for, before
// they have an account.
func (s *Server) previewInvite(w http.ResponseWriter, r *http.Request) error {
	inv, err := s.Store.Invite(r.Context(), r.PathValue("token"))
	if err != nil {
		return err
	}
	org, err := s.Store.OrgByID(r.Context(), inv.OrgID)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"org": map[string]string{"name": org.Name, "slug": org.Slug}, "role": inv.Role, "usable": inv.Usable(),
	})
	return nil
}

func (s *Server) acceptInvite(w http.ResponseWriter, r *http.Request, u *store.User) error {
	org, err := s.Store.AcceptInvite(r.Context(), r.PathValue("token"), u.ID)
	if errors.Is(err, store.ErrInviteUnusable) {
		return &Error{Status: http.StatusGone, Code: "invite_unusable", Message: "This invite has expired or been used up."}
	}
	if err != nil {
		return err
	}
	s.audit(r.Context(), u, org, "invite.accept", u.Username, "")
	s.syncOpsSoon(org)
	writeJSON(w, http.StatusOK, org)
	return nil
}
