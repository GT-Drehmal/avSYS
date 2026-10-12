package backup

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

func TestSnapshotFromResticJSON(t *testing.T) {
	const listed = `[{"time":"2026-10-12T01:11:04.5Z","hostname":"survival","id":"a8878ae3ffff",
	  "tags":["avsys","by=steve","loader=paper","trigger=manual","version=1.21.4","stray"],
	  "summary":{"total_bytes_processed":7000,"total_files_processed":12,"data_added_packed":1402}},
	 {"time":"2020-01-01T00:00:00Z","hostname":"old","id":"00000000aaaa"}]`
	var raw []resticSnapshot
	if err := json.Unmarshal([]byte(listed), &raw); err != nil {
		t.Fatal(err)
	}
	got := raw[0].snapshot()
	want := Snapshot{
		ID: "a8878ae3ffff", Time: time.Date(2026, 10, 12, 1, 11, 4, 500000000, time.UTC),
		Server: "survival", Trigger: "manual", By: "steve", Version: "1.21.4", Loader: "paper",
		Size: 7000, Added: 1402, Files: 12,
	}
	if got != want {
		t.Errorf("got %+v\nwant %+v", got, want)
	}
	// A snapshot made by hand with plain restic still lists.
	if plain := raw[1].snapshot(); plain.Server != "old" || plain.Trigger != TriggerManual || plain.Size != 0 {
		t.Errorf("snapshot without tags or summary: %+v", plain)
	}
}

func TestFollowReadsProgressAndErrors(t *testing.T) {
	var seen []Progress
	f := &follow{progress: func(p Progress) { seen = append(seen, p) }}
	for _, line := range []string{
		`{"message_type":"status","percent_done":0.25,"bytes_done":250,"total_bytes":1000}`,
		`{"message_type":"error","error":{"message":"permission denied"},"item":"/data/world/x"}`,
		`not json at all`,
		`{"message_type":"status","percent_done":0.5,"bytes_restored":500,"total_bytes":1000}`,
		`{"message_type":"summary","snapshot_id":"abc123"}`,
	} {
		f.line([]byte(line))
	}
	if len(seen) != 2 || seen[0] != (Progress{Percent: 25, Bytes: 250, Total: 1000}) || seen[1] != (Progress{Percent: 50, Bytes: 500, Total: 1000}) {
		t.Errorf("progress: %+v", seen)
	}
	if f.snapshotID != "abc123" {
		t.Errorf("snapshot id: %q", f.snapshotID)
	}
	err := f.failure("ignored stderr", errors.New("exit 1"))
	if err == nil || !strings.Contains(err.Error(), "/data/world/x: permission denied") {
		t.Errorf("failure: %v", err)
	}
}

func TestQuote(t *testing.T) {
	for in, want := range map[string]string{
		"plain":       `'plain'`,
		"two words":   `'two words'`,
		"it's":        `'it'\''s'`,
		"$(rm -rf /)": `'$(rm -rf /)'`,
	} {
		if got := quote(in); got != want {
			t.Errorf("quote(%q) = %s, want %s", in, got, want)
		}
	}
}

// workspace starts a real workspace container and connects to it the way the
// panel does. The test is skipped without Docker, restic, or the image named
// by AVSYS_TEST_WORKSPACE_IMAGE.
func workspace(t *testing.T) (*ssh.Client, *Manager) {
	t.Helper()
	image := os.Getenv("AVSYS_TEST_WORKSPACE_IMAGE")
	if image == "" {
		t.Skip("set AVSYS_TEST_WORKSPACE_IMAGE to a workspace image to run this")
	}
	for _, tool := range []string{"docker", "restic"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s is not installed", tool)
		}
	}
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(private)
	if err != nil {
		t.Fatal(err)
	}
	keys := t.TempDir()
	if err := os.WriteFile(filepath.Join(keys, "authorized_keys"), ssh.MarshalAuthorizedKey(signer.PublicKey()), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(keys, 0o755); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("docker", "run", "-d", "--rm", "-u", "1000:1000",
		"-e", "AVSYS_SERVER=probe",
		"-v", keys+":/etc/avsys/keys:ro",
		"--tmpfs", "/data:uid=1000,gid=1000,exec",
		"-p", "127.0.0.1::2222", image).CombinedOutput()
	if err != nil {
		t.Fatalf("starting the workspace: %v: %s", err, out)
	}
	id := strings.TrimSpace(string(out))
	t.Cleanup(func() { _ = exec.Command("docker", "rm", "-f", id).Run() })

	port, err := exec.Command("docker", "port", id, "2222/tcp").Output()
	if err != nil {
		t.Fatal(err)
	}
	addr := strings.TrimSpace(strings.Split(string(port), "\n")[0])
	cfg := &ssh.ClientConfig{
		User: "mc", Auth: []ssh.AuthMethod{ssh.PublicKeys(signer)},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(), Timeout: 5 * time.Second,
	}
	var client *ssh.Client
	for attempt := 0; attempt < 40; attempt++ {
		if client, err = ssh.Dial("tcp", addr, cfg); err == nil {
			break
		}
		time.Sleep(250 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("connecting to the workspace: %v", err)
	}
	t.Cleanup(func() { client.Close() })
	return client, &Manager{Dir: t.TempDir(), Restic: "restic", Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
}

func sh(t *testing.T, ws *ssh.Client, command string) string {
	t.Helper()
	var out bytes.Buffer
	stderr, err := run(ws, command, nil, func(line []byte) { out.Write(line); out.WriteByte('\n') })
	if err != nil {
		t.Fatalf("%s: %v: %s", command, err, stderr)
	}
	return strings.TrimSpace(out.String())
}

func TestSnapshotAndRestoreThroughAWorkspace(t *testing.T) {
	ws, m := workspace(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	sh(t, ws, `mkdir -p /data/world /data/mods /data/.workspace /data/.cache &&
		echo one > /data/world/level.dat && echo jar > /data/mods/a.jar &&
		echo history > /data/.workspace/.bash_history && echo cached > /data/.cache/x`)

	if list, err := m.List(ctx, "alpha"); err != nil || len(list) != 0 {
		t.Fatalf("an organization with no repository: %v, %v", list, err)
	}

	var reported int
	record := map[string]any{"name": "probe", "spec": map[string]any{"version": "1.21.4"}}
	first, err := m.Snapshot(ctx, "alpha", ws, Meta{Server: "probe", Trigger: TriggerManual, By: "steve", Version: "1.21.4", Loader: "paper", Record: record},
		func(Progress) { reported++ })
	if err != nil {
		t.Fatalf("first snapshot: %v", err)
	}
	if first.Server != "probe" || first.Trigger != TriggerManual || first.By != "steve" || first.Version != "1.21.4" || first.Loader != "paper" || first.Files < 3 {
		t.Errorf("first snapshot: %+v", first)
	}

	// The record of the server is in the snapshot, readable without the workspace.
	stored, err := m.Record(ctx, "alpha", first.ID)
	if err != nil || !strings.Contains(string(stored), `"version": "1.21.4"`) {
		t.Errorf("stored record: %s, %v", stored, err)
	}
	// A prefix of the ID is enough, as long as it names one snapshot.
	if short, err := m.Get(ctx, "alpha", first.ID[:8]); err != nil || short.ID != first.ID {
		t.Errorf("lookup by prefix: %+v, %v", short, err)
	}
	if _, err := m.Get(ctx, "alpha", "deadbeef"); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown snapshot: %v", err)
	}
	if _, err := m.Get(ctx, "alpha", "latest; rm -rf /"); !errors.Is(err, ErrNotFound) {
		t.Errorf("an ID that is not one: %v", err)
	}
	// Nothing is left listening in the workspace afterwards.
	if left := sh(t, ws, "ls /tmp/avsys | grep -c sock || true"); left != "0" {
		t.Errorf("%s sockets left behind", left)
	}

	// The world moves on, things appear and disappear, then a second snapshot.
	sh(t, ws, `echo two > /data/world/level.dat && echo stray > /data/world/stray.txt &&
		rm /data/mods/a.jar && mkdir /data/new && echo x > /data/new/f &&
		echo more >> /data/.workspace/.bash_history && echo outside > /tmp/outside.txt`)
	second, err := m.Snapshot(ctx, "alpha", ws, Meta{Server: "probe", Trigger: TriggerPreRestore, Record: record}, nil)
	if err != nil {
		t.Fatalf("second snapshot: %v", err)
	}
	list, err := m.List(ctx, "alpha")
	if err != nil || len(list) != 2 || list[0].ID != second.ID || list[1].ID != first.ID {
		t.Fatalf("list after two snapshots: %+v, %v", list, err)
	}
	if list[0].Trigger != TriggerPreRestore || list[0].By != "" {
		t.Errorf("second snapshot: %+v", list[0])
	}

	// Another organization sees none of this.
	if other, err := m.List(ctx, "beta"); err != nil || len(other) != 0 {
		t.Errorf("another organization's list: %v, %v", other, err)
	}

	if err := m.Restore(ctx, "alpha", ws, first.ID, nil); err != nil {
		t.Fatalf("restore: %v", err)
	}
	state := sh(t, ws, `cat /data/world/level.dat; cat /data/mods/a.jar;
		test -e /data/world/stray.txt && echo STRAY; test -e /data/new && echo NEWDIR;
		wc -l < /data/.workspace/.bash_history; cat /data/.cache/x; cat /tmp/outside.txt; test -x /bin/sh && echo shell`)
	want := "one\njar\n2\ncached\noutside\nshell"
	if state != want {
		t.Errorf("after restore:\n%s\nwant:\n%s", state, want)
	}

	// Usage counts both snapshots and is not empty.
	usage, err := m.Usage(ctx, "alpha")
	if err != nil || usage.Snapshots != 2 || usage.Bytes == 0 {
		t.Errorf("usage: %+v, %v", usage, err)
	}

	// Only the panel removes a snapshot.
	if err := m.Delete(ctx, "alpha", second.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if list, _ := m.List(ctx, "alpha"); len(list) != 1 || list[0].ID != first.ID {
		t.Errorf("list after delete: %+v", list)
	}
}

// What a workspace is given can be added to and read, never deleted from,
// even by someone who has the repository password.
func TestAWorkspaceCannotDeleteSnapshots(t *testing.T) {
	ws, m := workspace(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	sh(t, ws, "mkdir -p /data/world && echo one > /data/world/level.dat")
	snap, err := m.Snapshot(ctx, "alpha", ws, Meta{Server: "probe", Trigger: TriggerManual, Record: map[string]any{}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	secret, err := m.password("alpha")
	if err != nil {
		t.Fatal(err)
	}
	socket, stop, err := m.serve(ws, "alpha")
	if err != nil {
		t.Fatal(err)
	}
	defer stop()

	for _, attempt := range [][]string{
		{"forget", snap.ID},
		{"forget", "--prune", snap.ID},
	} {
		stderr, err := run(ws, resticIn(socket, attempt...), strings.NewReader(secret+"\n"), nil)
		if err == nil {
			t.Errorf("restic %s through the workspace succeeded: %s", strings.Join(attempt, " "), stderr)
		}
	}
	// Nor by speaking to the repository directly, without restic: neither
	// removing a snapshot nor writing over one.
	for _, request := range []string{"-X DELETE", "-X POST --data-binary junk"} {
		code := sh(t, ws, "curl -s -o /dev/null -w '%{http_code}' --unix-socket "+quote(socket)+" "+request+" http://repository/snapshots/"+snap.ID)
		if code != "403" {
			t.Errorf("curl %s on a snapshot: HTTP %s, want 403", request, code)
		}
	}
	// Reading still works, which is what a restore needs.
	if _, err := run(ws, resticIn(socket, "snapshots", "--json"), strings.NewReader(secret+"\n"), nil); err != nil {
		t.Errorf("listing through the workspace: %v", err)
	}
	if list, err := m.List(ctx, "alpha"); err != nil || len(list) != 1 || list[0].ID != snap.ID {
		t.Errorf("the snapshot did not survive: %+v, %v", list, err)
	}
	// Whatever those attempts left locked must not get in the panel's way.
	if _, err := m.Usage(ctx, "alpha"); err != nil {
		t.Errorf("usage after the attempts: %v", err)
	}
}

// A snapshot that finishes while a delete is waiting for the repository must
// not wait on that delete, which is itself waiting on the snapshot.
func TestSnapshotFinishesWhileADeleteWaits(t *testing.T) {
	ws, m := workspace(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	sh(t, ws, "mkdir -p /data/world && echo one > /data/world/level.dat")
	old, err := m.Snapshot(ctx, "alpha", ws, Meta{Server: "probe", Trigger: TriggerManual, Record: map[string]any{}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	// Enough new data that restic reports progress along the way.
	sh(t, ws, "head -c 60000000 /dev/urandom > /data/world/region.bin")

	type result struct {
		what string
		err  error
	}
	done := make(chan result, 2)
	started := make(chan struct{})
	go func() {
		var once sync.Once
		_, err := m.Snapshot(ctx, "alpha", ws, Meta{Server: "probe", Trigger: TriggerManual, Record: map[string]any{}}, func(Progress) {
			// Holding up the first report holds the snapshot open, with the
			// repository in use, for long enough that the delete below is
			// certainly waiting for it by the time the snapshot wraps up.
			once.Do(func() {
				close(started)
				time.Sleep(4 * time.Second)
			})
		})
		done <- result{"snapshot", err}
	}()
	select {
	case <-started:
	case <-time.After(time.Minute):
		t.Fatal("the snapshot never reported progress")
	}
	go func() { done <- result{"delete", m.Delete(ctx, "alpha", old.ID)} }()

	for range 2 {
		select {
		case r := <-done:
			if r.err != nil {
				t.Errorf("%s: %v", r.what, r.err)
			}
		case <-time.After(45 * time.Second):
			t.Fatal("the snapshot and the delete are waiting on each other")
		}
	}
	if list, err := m.List(ctx, "alpha"); err != nil || len(list) != 1 || list[0].ID == old.ID {
		t.Errorf("after both: %+v, %v", list, err)
	}
}

// A snapshot that could not read every file is not kept. Restoring one would
// delete the files it had missed.
func TestAnIncompleteSnapshotIsDiscarded(t *testing.T) {
	ws, m := workspace(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	sh(t, ws, `mkdir -p /data/world && echo fine > /data/world/level.dat &&
		echo secret > /data/world/unreadable.dat && chmod 000 /data/world/unreadable.dat`)
	snap, err := m.Snapshot(ctx, "alpha", ws, Meta{Server: "probe", Trigger: TriggerManual, Record: map[string]any{}}, nil)
	if !errors.Is(err, ErrIncomplete) {
		t.Fatalf("snapshot with an unreadable file: %+v, %v; want ErrIncomplete", snap, err)
	}
	if !strings.Contains(err.Error(), "unreadable.dat") {
		t.Errorf("the error does not name the file: %v", err)
	}
	if list, err := m.List(ctx, "alpha"); err != nil || len(list) != 0 {
		t.Errorf("an incomplete snapshot was kept: %+v, %v", list, err)
	}

	// Once the file can be read, the snapshot is taken and holds it.
	sh(t, ws, "chmod 600 /data/world/unreadable.dat")
	whole, err := m.Snapshot(ctx, "alpha", ws, Meta{Server: "probe", Trigger: TriggerManual, Record: map[string]any{}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	sh(t, ws, "rm /data/world/unreadable.dat")
	if err := m.Restore(ctx, "alpha", ws, whole.ID, nil); err != nil {
		t.Fatal(err)
	}
	if got := sh(t, ws, "cat /data/world/unreadable.dat"); got != "secret" {
		t.Errorf("after restoring the whole snapshot: %q", got)
	}
}

// Whether an organization has snapshots is "unknown", not "none", whenever
// something is there that cannot be read.
func TestHolds(t *testing.T) {
	ctx := context.Background()
	m := &Manager{Dir: t.TempDir(), Restic: "restic-is-not-installed", Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	check := func(org string, wantCount int, wantKnown bool) {
		t.Helper()
		if count, known := m.Holds(ctx, org); count != wantCount || known != wantKnown {
			t.Errorf("Holds(%s) = %d, %v; want %d, %v", org, count, known, wantCount, wantKnown)
		}
	}
	write := func(path string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	check("nothing-there", 0, true)

	if err := os.MkdirAll(filepath.Join(m.Dir, "empty-directory"), 0o700); err != nil {
		t.Fatal(err)
	}
	check("empty-directory", 0, true)

	// A repository this panel has no restic to read.
	write(filepath.Join(m.Dir, "unreadable", "config"))
	check("unreadable", 0, false)

	// One that lost its configuration but still has data in it.
	write(filepath.Join(m.Dir, "damaged", "data", "ab", "abcdef"))
	check("damaged", 0, false)

	// A password with no repository beside it.
	write(filepath.Join(m.Dir, "key-only.key"))
	check("key-only", 0, false)
}
