package store

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Domains an organization claims are its own: a server's hostname equal to
// or under one of them can only be another server of that organization's.
// A domain has one owner, and no two organizations' claims overlap.

// ClaimedError says a domain overlaps what another organization claims.
type ClaimedError struct {
	Domain, Claimed, OrgID string
}

func (e *ClaimedError) Error() string {
	return fmt.Sprintf("%s overlaps %s, which another organization claims", e.Domain, e.Claimed)
}

// Overlap reports whether two domains are the same or one is under the other.
func Overlap(a, b string) bool {
	return a == b || strings.HasSuffix(a, "."+b) || strings.HasSuffix(b, "."+a)
}

// Domains lists an organization's domains, in order.
func (s *Store) Domains(ctx context.Context, orgID string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT domain FROM org_domains WHERE org_id = ? ORDER BY domain`, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var d string
		if err := rows.Scan(&d); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// DomainOwner returns the organization that claims host or a domain it is
// under, or "" if none does.
func (s *Store) DomainOwner(ctx context.Context, host string) (string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT domain, org_id FROM org_domains`)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	for rows.Next() {
		var d, org string
		if err := rows.Scan(&d, &org); err != nil {
			return "", err
		}
		if host == d || strings.HasSuffix(host, "."+d) {
			return org, nil
		}
	}
	return "", rows.Err()
}

// SetDomains replaces an organization's domains. None of them may overlap
// another organization's; that is checked and written in one transaction.
func (s *Store) SetDomains(ctx context.Context, orgID string, domains []string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT domain, org_id FROM org_domains WHERE org_id != ?`, orgID)
	if err != nil {
		return err
	}
	type claim struct{ domain, org string }
	var others []claim
	for rows.Next() {
		var c claim
		if err := rows.Scan(&c.domain, &c.org); err != nil {
			rows.Close()
			return err
		}
		others = append(others, c)
	}
	rows.Close()
	sort.Strings(domains)
	for _, d := range domains {
		for _, o := range others {
			if Overlap(d, o.domain) {
				return &ClaimedError{Domain: d, Claimed: o.domain, OrgID: o.org}
			}
		}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM org_domains WHERE org_id = ?`, orgID); err != nil {
		return err
	}
	now := time.Now().Unix()
	for _, d := range domains {
		if _, err := tx.ExecContext(ctx, `INSERT INTO org_domains (domain, org_id, created_at) VALUES (?, ?, ?)`, d, orgID, now); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// IsClaimed reports whether err is a ClaimedError.
func IsClaimed(err error) (*ClaimedError, bool) {
	var c *ClaimedError
	ok := errors.As(err, &c)
	return c, ok
}
