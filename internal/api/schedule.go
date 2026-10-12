package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"sync"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/client"

	avsysv1 "github.com/GT-Drehmal/avSYS/api/v1alpha1"
	"github.com/GT-Drehmal/avSYS/internal/backup"
	"github.com/GT-Drehmal/avSYS/internal/store"
)

// backupPolicy is an organization's rule for snapshots nobody has to ask
// for: every server once a day, and how long those are kept.
//
// Only scheduled snapshots are ever removed by it. Ones taken by hand, undo
// points and every snapshot of a server that has been deleted stay until
// someone deletes them.
type backupPolicy struct {
	Enabled bool `json:"enabled"`
	// Minute of the day, in UTC, at which the daily snapshots begin.
	Minute int         `json:"minute"`
	Keep   backup.Keep `json:"keep"`
}

// defaultPolicy applies to an organization that has not set one. 09:00 UTC is
// the small hours across the Americas.
var defaultPolicy = backupPolicy{Enabled: true, Minute: 9 * 60, Keep: backup.Keep{Daily: 7, Weekly: 4, Monthly: 12, Yearly: 2}}

func (p backupPolicy) validate() error {
	if p.Minute < 0 || p.Minute >= 24*60 {
		return errBadRequest("The time of day is given as minutes after midnight UTC, from 0 to 1439.")
	}
	for _, limit := range []struct {
		name     string
		n, most  int
		atLeast1 bool
	}{
		{"daily", p.Keep.Daily, 90, true},
		{"weekly", p.Keep.Weekly, 104, false},
		{"monthly", p.Keep.Monthly, 120, false},
		{"yearly", p.Keep.Yearly, 20, false},
	} {
		if limit.n < 0 || limit.n > limit.most || (limit.atLeast1 && limit.n < 1) {
			least := 0
			if limit.atLeast1 {
				least = 1
			}
			return errBadRequest("The number of %s snapshots to keep is from %d to %d.", limit.name, least, limit.most)
		}
	}
	return nil
}

// lastDue is the most recent moment, at or before now, at which the daily
// snapshots were to begin.
func (p backupPolicy) lastDue(now time.Time) time.Time {
	now = now.UTC()
	due := time.Date(now.Year(), now.Month(), now.Day(), p.Minute/60, p.Minute%60, 0, 0, time.UTC)
	if due.After(now) {
		due = due.AddDate(0, 0, -1)
	}
	return due
}

// owed reports whether a server has yet to be given the run that was due
// then. A server made after that moment waits for the next one, so that
// making a server is not followed a minute later by a snapshot of it.
func owed(mc *avsysv1.MinecraftServer, due time.Time) bool {
	if mc.CreationTimestamp.Time.After(due) {
		return false
	}
	st := mc.Status.Snapshots
	return st == nil || st.LastRun == nil || st.LastRun.Time.Before(due)
}

func policyKey(orgID string) string { return "backup-policy/" + orgID }

func (s *Server) policyOf(ctx context.Context, org *store.Org) (backupPolicy, error) {
	raw, err := s.Store.Setting(ctx, policyKey(org.ID))
	if errors.Is(err, store.ErrNotFound) {
		return defaultPolicy, nil
	}
	if err != nil {
		return backupPolicy{}, err
	}
	policy := defaultPolicy
	if err := json.Unmarshal([]byte(raw), &policy); err != nil || policy.validate() != nil {
		s.Log.Warn("an organization's stored backup policy is unreadable; using the default", "org", org.Slug)
		return defaultPolicy, nil
	}
	return policy, nil
}

// policyView is a policy with when it will next act.
type policyView struct {
	backupPolicy
	// Next is when the daily snapshots next begin, absent when they are off.
	Next *time.Time `json:"next,omitempty"`
}

func viewPolicy(p backupPolicy, now time.Time) policyView {
	v := policyView{backupPolicy: p}
	if p.Enabled {
		next := p.lastDue(now).AddDate(0, 0, 1)
		v.Next = &next
	}
	return v
}

func (s *Server) getBackupPolicy(w http.ResponseWriter, r *http.Request, u *store.User) error {
	a, err := s.org(r, u, store.RoleViewer)
	if err != nil {
		return err
	}
	policy, err := s.policyOf(r.Context(), a.Org)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, viewPolicy(policy, time.Now()))
	return nil
}

// putBackupPolicy replaces the organization's policy whole.
func (s *Server) putBackupPolicy(w http.ResponseWriter, r *http.Request, u *store.User) error {
	a, err := s.org(r, u, store.RoleAdmin)
	if err != nil {
		return err
	}
	var policy backupPolicy
	if err := decode(r, &policy); err != nil {
		return err
	}
	if err := policy.validate(); err != nil {
		return err
	}
	raw, err := json.Marshal(policy)
	if err != nil {
		return err
	}
	if err := s.Store.SetSetting(r.Context(), policyKey(a.Org.ID), string(raw)); err != nil {
		return err
	}
	s.audit(r.Context(), u, a.Org, "backups.policy", a.Org.Slug, string(raw))
	writeJSON(w, http.StatusOK, viewPolicy(policy, time.Now()))
	return nil
}

// schedule keeps what the daily snapshots need to remember between looks.
type schedule struct {
	mu sync.Mutex
	// running holds the organizations a run is going through right now.
	running map[string]bool
	// failed is when a server's scheduled snapshot last failed. It is tried
	// again an hour later, not every minute.
	failed map[string]time.Time
}

const retryFailedAfter = time.Hour

// claim marks an organization as being run through. ok is false if it is already.
func (sc *schedule) claim(orgID string) (release func(), ok bool) {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	if sc.running == nil {
		sc.running, sc.failed = map[string]bool{}, map[string]time.Time{}
	}
	if sc.running[orgID] {
		return nil, false
	}
	sc.running[orgID] = true
	return func() {
		sc.mu.Lock()
		defer sc.mu.Unlock()
		delete(sc.running, orgID)
	}, true
}

func (sc *schedule) failedRecently(key string, now time.Time) bool {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	at, ok := sc.failed[key]
	return ok && now.Sub(at) < retryFailedAfter
}

func (sc *schedule) note(key string, failed bool, now time.Time) {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	if sc.failed == nil {
		sc.running, sc.failed = map[string]bool{}, map[string]time.Time{}
	}
	if failed {
		sc.failed[key] = now
	} else {
		delete(sc.failed, key)
	}
}

// RunSchedule takes the daily snapshots. It looks once a minute for servers
// that are owed one and goes through them one at a time, until ctx ends. A
// panel that was not running when they were due takes them when it is back.
func (s *Server) RunSchedule(ctx context.Context) {
	for {
		s.scheduleTick(ctx, time.Now())
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Minute):
		}
	}
}

func (s *Server) scheduleTick(ctx context.Context, now time.Time) {
	if !s.Backups.Available() {
		return
	}
	for _, group := range s.owedServers(ctx, now) {
		if ctx.Err() != nil {
			return
		}
		release, ok := s.schedule.claim(group.org.ID)
		if !ok {
			continue
		}
		s.runScheduled(ctx, group.org, group.policy, group.servers)
		release()
	}
}

type owedGroup struct {
	org     *store.Org
	policy  backupPolicy
	servers []*avsysv1.MinecraftServer
}

// owedServers finds every server that is owed a scheduled snapshot now, by
// organization.
func (s *Server) owedServers(ctx context.Context, now time.Time) []owedGroup {
	var all avsysv1.MinecraftServerList
	if err := s.Client.List(ctx, &all); err != nil {
		s.Log.Error("looking for servers owed a snapshot", "error", err)
		return nil
	}
	byNamespace := map[string][]*avsysv1.MinecraftServer{}
	for i := range all.Items {
		byNamespace[all.Items[i].Namespace] = append(byNamespace[all.Items[i].Namespace], &all.Items[i])
	}
	namespaces := make([]string, 0, len(byNamespace))
	for ns := range byNamespace {
		namespaces = append(namespaces, ns)
	}
	sort.Strings(namespaces)

	var groups []owedGroup
	for _, ns := range namespaces {
		slug, ok := s.orgOfNamespace(ns)
		if !ok {
			continue
		}
		org, err := s.Store.OrgBySlug(ctx, slug)
		if err != nil {
			continue
		}
		policy, err := s.policyOf(ctx, org)
		if err != nil || !policy.Enabled {
			continue
		}
		due := policy.lastDue(now)
		group := owedGroup{org: org, policy: policy}
		for _, mc := range byNamespace[ns] {
			if owed(mc, due) && !s.schedule.failedRecently(ns+"/"+mc.Name, now) {
				group.servers = append(group.servers, mc)
			}
		}
		if len(group.servers) > 0 {
			sort.Slice(group.servers, func(i, j int) bool { return group.servers[i].Name < group.servers[j].Name })
			groups = append(groups, group)
		}
	}
	return groups
}

// scheduleResult is what a run did for one server.
type scheduleResult struct {
	Server string `json:"server"`
	// Outcome is "snapshot", "unchanged" when nothing had changed since the
	// last one, "skipped" when the server could not be snapshotted just now,
	// or "failed".
	Outcome  string           `json:"outcome"`
	Snapshot *backup.Snapshot `json:"snapshot,omitempty"`
	// Reason says why, for skipped and failed.
	Reason string `json:"reason,omitempty"`
	// Removed is how many older scheduled snapshots the policy let go of.
	Removed int `json:"removed,omitempty"`
}

// runScheduled gives each of the servers its scheduled snapshot, one after
// another, and then lets go of the ones the policy no longer keeps.
func (s *Server) runScheduled(ctx context.Context, org *store.Org, policy backupPolicy, servers []*avsysv1.MinecraftServer) []scheduleResult {
	results := make([]scheduleResult, 0, len(servers))
	removed := 0
	for _, mc := range servers {
		if ctx.Err() != nil {
			break
		}
		res := s.scheduledSnapshot(ctx, org, mc, policy.Keep)
		removed += res.Removed
		results = append(results, res)
	}
	// Forgetting a snapshot only unlists it. The space comes back here, once
	// for the organization, because it means going through the whole store.
	if removed > 0 {
		if err := s.Backups.Prune(context.WithoutCancel(ctx), org.Slug); err != nil {
			s.Log.Error("could not reclaim the space of expired snapshots", "org", org.Slug, "error", err)
		}
		s.Events.Publish(Event{Type: "operation.done", Namespace: s.Namespace(org.Slug), Data: operationDone{Kind: "delete", Org: org.Slug, Scheduled: true}})
	}
	return results
}

func (s *Server) scheduledSnapshot(ctx context.Context, org *store.Org, mc *avsysv1.MinecraftServer, keep backup.Keep) scheduleResult {
	res := scheduleResult{Server: mc.Name, Outcome: "skipped"}
	// As it is now: the list this came from may be a while old by the time
	// its turn comes.
	mc, err := s.fresh(ctx, mc)
	if err != nil {
		res.Reason = "The server is gone."
		return res
	}
	if err := unfinished(mc); err != nil {
		res.Reason = err.Error()
		return res
	}
	if err := settled(mc); err != nil {
		res.Reason = err.Error()
		return res
	}
	op, ok := s.ops.begin(mc.Namespace, org.Slug, mc.Name, kindSnapshot, "")
	if !ok {
		res.Reason = errBusy.Message
		return res
	}
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	snap, err := s.takeSnapshot(ctx, org.Slug, mc, op, backup.TriggerScheduled, "")
	key := mc.Namespace + "/" + mc.Name
	switch {
	case errors.Is(err, backup.ErrUnchanged):
		res.Outcome, err = "unchanged", nil
	case err != nil:
		res.Outcome, res.Reason = "failed", operationFailed(err).Error()
	default:
		res.Outcome, res.Snapshot = "snapshot", snap
	}
	s.schedule.note(key, err != nil, time.Now())
	s.finish(ctx, mc.Namespace, mc.Name, kindSnapshot, snap, snap, err, true)
	if err != nil {
		return res
	}
	// Retention is applied whenever the server's run completes, also when it
	// took nothing new: the policy may have been tightened since.
	removed, err := s.Backups.Forget(context.WithoutCancel(ctx), org.Slug, mc.Name, keep)
	if err != nil {
		s.Log.Error("could not apply the retention policy", "org", org.Slug, "server", mc.Name, "error", err)
	}
	res.Removed = removed
	return res
}

// runBackupPolicy gives every server in the organization its scheduled
// snapshot now, whether or not one is due and whether or not the daily
// snapshots are switched on. It answers at once; with ?wait=true it answers
// when the run is over, with what it did.
func (s *Server) runBackupPolicy(w http.ResponseWriter, r *http.Request, u *store.User) error {
	a, err := s.org(r, u, store.RoleAdmin)
	if err != nil {
		return err
	}
	if !s.Backups.Available() {
		return errNoBackups
	}
	policy, err := s.policyOf(r.Context(), a.Org)
	if err != nil {
		return err
	}
	var list avsysv1.MinecraftServerList
	if err := s.Client.List(r.Context(), &list, client.InNamespace(a.Namespace)); err != nil {
		return err
	}
	servers := make([]*avsysv1.MinecraftServer, 0, len(list.Items))
	for i := range list.Items {
		servers = append(servers, &list.Items[i])
	}
	sort.Slice(servers, func(i, j int) bool { return servers[i].Name < servers[j].Name })
	release, ok := s.schedule.claim(a.Org.ID)
	if !ok {
		return &Error{Status: http.StatusConflict, Code: "busy", Message: "The scheduled snapshots of this organization are being taken right now."}
	}
	s.audit(r.Context(), u, a.Org, "backups.run", a.Org.Slug, fmt.Sprintf("%d servers", len(servers)))
	work := func() []scheduleResult {
		defer release()
		return s.runScheduled(context.WithoutCancel(r.Context()), a.Org, policy, servers)
	}
	if r.URL.Query().Get("wait") == "true" {
		writeJSON(w, http.StatusOK, map[string]any{"results": work()})
		return nil
	}
	go work()
	writeJSON(w, http.StatusAccepted, map[string]any{"servers": len(servers)})
	return nil
}
