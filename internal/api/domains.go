package api

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"sigs.k8s.io/controller-runtime/pkg/client"

	avsysv1 "github.com/GT-Drehmal/avSYS/api/v1alpha1"
	"github.com/GT-Drehmal/avSYS/internal/router"
	"github.com/GT-Drehmal/avSYS/internal/store"
)

// An organization's domains are given it by the panel's administrator, who
// is also who points their DNS at the router. A hostname equal to or under
// one of them is that organization's alone; one under no claimed domain is
// anyone's, so a panel with a single organization needs none of this.

func normalizeDomain(d string) (string, error) {
	d = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(d)), ".")
	if !hostnamePattern.MatchString(d) {
		return "", errBadRequest("%q is not a domain, such as play.example.org.", d)
	}
	return d, nil
}

func (s *Server) getDomains(w http.ResponseWriter, r *http.Request, u *store.User) error {
	a, err := s.org(r, u, store.RoleViewer)
	if err != nil {
		return err
	}
	domains, err := s.Store.Domains(r.Context(), a.Org.ID)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"domains": domains, "editable": u.IsAdmin})
	return nil
}

type hostnameInUse struct {
	Org      string `json:"org"`
	Server   string `json:"server"`
	Hostname string `json:"hostname"`
}

func (s *Server) putDomains(w http.ResponseWriter, r *http.Request, u *store.User) error {
	a, err := s.org(r, u, store.RoleViewer)
	if err != nil {
		return err
	}
	if !u.IsAdmin {
		return errForbidden
	}
	var in struct {
		Domains []string `json:"domains"`
	}
	if err := decode(r, &in); err != nil {
		return err
	}
	seen := map[string]bool{}
	domains := []string{}
	for _, d := range in.Domains {
		n, err := normalizeDomain(d)
		if err != nil {
			return err
		}
		if !seen[n] {
			seen[n] = true
			domains = append(domains, n)
		}
	}
	s.hostnames.Lock()
	defer s.hostnames.Unlock()
	// Servers of other organizations already under one of them keep working;
	// whoever claimed it is told which.
	inUse := []hostnameInUse{}
	// As the cluster is now: a server given a name a moment ago is in it.
	var reader client.Reader = s.Client
	if s.Reader != nil {
		reader = s.Reader
	}
	var all avsysv1.MinecraftServerList
	if err := reader.List(r.Context(), &all); err != nil {
		return err
	}
	for _, mc := range all.Items {
		host := router.NormalizeHostname(mc.Spec.Expose.Hostname)
		if host == "" || mc.Namespace == a.Namespace {
			continue
		}
		for _, d := range domains {
			if host == d || strings.HasSuffix(host, "."+d) {
				org, _ := s.orgOfNamespace(mc.Namespace)
				inUse = append(inUse, hostnameInUse{Org: org, Server: mc.Name, Hostname: host})
			}
		}
	}
	if err := s.Store.SetDomains(r.Context(), a.Org.ID, domains); err != nil {
		if c, ok := store.IsClaimed(err); ok {
			other := "another organization"
			if o, err := s.Store.OrgByID(r.Context(), c.OrgID); err == nil {
				other = o.Name
			}
			return &Error{Status: http.StatusConflict, Code: "domain_claimed", Message: fmt.Sprintf("%s overlaps %s, which %s has. A domain has one organization.", c.Domain, c.Claimed, other)}
		}
		return err
	}
	s.audit(r.Context(), u, a.Org, "org.domains", a.Org.Slug, strings.Join(domains, " "))
	sort.Slice(inUse, func(i, j int) bool { return inUse[i].Hostname < inUse[j].Hostname })
	writeJSON(w, http.StatusOK, map[string]any{"domains": domains, "editable": true, "inUse": inUse})
	return nil
}

// withHostname does write, which gives a server host, if host may be its
// organization's: checked and written while no domain is being claimed.
func (s *Server) withHostname(ctx context.Context, org *store.Org, host string, write func() error) error {
	s.hostnames.Lock()
	defer s.hostnames.Unlock()
	if err := s.checkHostnameOwner(ctx, org, host); err != nil {
		return err
	}
	return write()
}

// checkHostnameOwner refuses a hostname under a domain another organization
// claims. The address a panel gives a server by itself, under its own
// organization's name in the router's domain, is always its own.
func (s *Server) checkHostnameOwner(ctx context.Context, org *store.Org, host string) error {
	if host == "" {
		return nil
	}
	// The names the panel gives by itself are under each organization's own
	// name in the router's domain: <name>.<org>.<domain>. Those are that
	// organization's alone, whatever is claimed, and whether or not it has
	// been made yet.
	if d := router.NormalizeHostname(s.Config.RouterDomain); d != "" && strings.HasSuffix(host, "."+d) {
		labels := strings.Split(strings.TrimSuffix(host, "."+d), ".")
		if zone := labels[len(labels)-1]; len(labels) > 1 {
			if zone == org.Slug {
				return nil
			}
			return &Error{Status: http.StatusConflict, Code: "domain_claimed", Message: fmt.Sprintf("%s is under %s.%s, where the panel names the servers of the organization %s.", host, zone, d, zone)}
		}
	}
	owner, err := s.Store.DomainOwner(ctx, host)
	if err != nil || owner == "" || owner == org.ID {
		return err
	}
	own, _ := s.Store.Domains(ctx, org.ID)
	hint := "Use a hostname under a domain this organization has, or ask the panel's administrator for one."
	if len(own) > 0 {
		hint = "This organization's are " + strings.Join(own, ", ") + "."
	}
	return &Error{Status: http.StatusConflict, Code: "domain_claimed", Message: fmt.Sprintf("%s is under a domain another organization has. %s", host, hint)}
}
