package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"time"

	"k8s.io/apimachinery/pkg/api/resource"

	avsysv1 "github.com/GT-Drehmal/avSYS/api/v1alpha1"
)

// A test that deletes snapshots changes what every other test sees of them,
// and runs in two browsers at once. So it makes an organization of its own
// and asks for snapshots in it here, by POST /uitest/snapshots?org=&server=&count=.
// They are kept one to a file under the repository's snapshots folder,
// named by their IDs as restic's are, which the stand-in restic lists and
// a forget removes: a removal is seen as it would be.

var seedName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

// seedOrgSnapshots answers POST /uitest/snapshots with the IDs of the
// snapshots it made, newest first.
func seedOrgSnapshots(dir string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		org, server := q.Get("org"), q.Get("server")
		count, err := strconv.Atoi(q.Get("count"))
		if !seedName.MatchString(org) || !seedName.MatchString(server) || err != nil || count < 1 || count > 20 {
			http.Error(w, "org, server and count (1 to 20)", http.StatusBadRequest)
			return
		}
		repo := filepath.Join(dir, "backups", org)
		if err := os.MkdirAll(filepath.Join(repo, "snapshots"), 0o755); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if err := os.WriteFile(filepath.Join(repo, "config"), nil, 0o600); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		type summary struct {
			Bytes int64 `json:"total_bytes_processed"`
			Files int64 `json:"total_files_processed"`
			Added int64 `json:"data_added_packed"`
		}
		type snapshot struct {
			ID       string    `json:"id"`
			Time     time.Time `json:"time"`
			Hostname string    `json:"hostname"`
			Tags     []string  `json:"tags"`
			Summary  summary   `json:"summary"`
		}
		now := time.Now().UTC()
		ids := []string{}
		for i := range count {
			raw := make([]byte, 32)
			_, _ = rand.Read(raw)
			s := snapshot{ID: hex.EncodeToString(raw), Time: now.Add(-time.Duration(i+1) * time.Hour), Hostname: server,
				Tags: []string{"avsys", "trigger=manual", "by=owner", "loader=paper", "version=1.21.4"}, Summary: summary{Bytes: 200 << 20, Files: 400, Added: 3 << 20}}
			data, _ := json.Marshal(s)
			if err := os.WriteFile(filepath.Join(repo, "snapshots", s.ID), data, 0o600); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			record, _ := json.Marshal(map[string]any{"format": 1, "org": org, "server": server, "spec": avsysv1.MinecraftServerSpec{
				EULA: true, DisplayName: server, Version: "1.21.4", Loader: avsysv1.LoaderPaper,
				Resources: avsysv1.ResourcesSpec{Memory: resource.MustParse("2Gi")},
				Storage:   avsysv1.StorageSpec{Size: resource.MustParse("2Gi")},
			}})
			if err := os.WriteFile(filepath.Join(repo, "record-"+s.ID+".json"), record, 0o600); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			ids = append(ids, s.ID)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"ids": ids})
	}
}
