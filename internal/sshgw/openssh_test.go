package sshgw

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime/pprof"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// sshdAt dials one sshd whatever pod is asked for.
type sshdAt string

func (a sshdAt) DialPod(ctx context.Context, _, _ string, _ int) (net.Conn, error) {
	var d net.Dialer
	return d.DialContext(ctx, "tcp", string(a))
}

// openSSH runs a real sshd, configured as a workspace's, behind a gateway,
// and returns how to reach it with the OpenSSH client: the arguments for ssh
// and the address to put after them. It skips the test where there is no
// OpenSSH or it cannot run.
func openSSH(t *testing.T) (sshArgs []string, target string) {
	t.Helper()
	sshd := "/usr/sbin/sshd"
	if _, err := os.Stat(sshd); err != nil {
		t.Skip("no sshd")
	}
	if _, err := exec.LookPath("ssh"); err != nil {
		t.Skip("no ssh")
	}
	me, err := user.Current()
	if err != nil {
		t.Skip(err)
	}
	dir := t.TempDir()

	hostKey := filepath.Join(dir, "host")
	if out, err := exec.Command("ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-f", hostKey).CombinedOutput(); err != nil {
		t.Skipf("ssh-keygen: %v %s", err, out)
	}
	wsKey := signer(t)
	if err := os.WriteFile(filepath.Join(dir, "authorized_keys"), ssh.MarshalAuthorizedKey(wsKey.PublicKey()), 0o600); err != nil {
		t.Fatal(err)
	}
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	block, err := ssh.MarshalPrivateKey(priv, "steve")
	if err != nil {
		t.Fatal(err)
	}
	steveKey := filepath.Join(dir, "steve")
	if err := os.WriteFile(steveKey, pem.EncodeToMemory(block), 0o600); err != nil {
		t.Fatal(err)
	}
	steve, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	config := filepath.Join(dir, "sshd_config")
	// As images/workspace/sshd_config has it.
	if err := os.WriteFile(config, []byte(fmt.Sprintf(`Port %d
ListenAddress 127.0.0.1
HostKey %s
PidFile %s/sshd.pid
AuthorizedKeysFile %s/authorized_keys
StrictModes no
UsePAM no
PasswordAuthentication no
KbdInteractiveAuthentication no
AllowUsers %s
AllowTcpForwarding yes
X11Forwarding no
PrintMotd no
PrintLastLog no
PermitUserRC no
Subsystem sftp internal-sftp
ClientAliveInterval 30
`, port, hostKey, dir, dir, me.Username)), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(sshd, "-D", "-e", "-f", config)
	var logs bytes.Buffer
	cmd.Stdout, cmd.Stderr = &logs, &logs
	if err := cmd.Start(); err != nil {
		t.Skipf("sshd: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		if t.Failed() {
			t.Logf("sshd said:\n%s", logs.String())
		}
	})
	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	for i := 0; ; i++ {
		c, err := net.Dial("tcp", addr)
		if err == nil {
			c.Close()
			break
		}
		if i == 50 {
			t.Skipf("sshd did not start: %s", logs.String())
		}
		time.Sleep(100 * time.Millisecond)
	}

	// Where sshd cannot sign anyone in here, there is nothing to test.
	direct, err := ssh.Dial("tcp", addr, &ssh.ClientConfig{User: me.Username, Auth: []ssh.AuthMethod{ssh.PublicKeys(wsKey)},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(), Timeout: 5 * time.Second})
	if err != nil {
		t.Skipf("sshd does not sign in here: %v\n%s", err, logs.String())
	}
	direct.Close()

	was := workspaceUser
	workspaceUser = me.Username
	t.Cleanup(func() { workspaceUser = was })
	gw, _ := gatewayTo(t, sshdAt(addr), wsKey, steve)
	host, gwPort, _ := net.SplitHostPort(gw)
	return []string{"ssh", "-p", gwPort, "-i", steveKey, "-o", "IdentitiesOnly=yes", "-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=/dev/null", "-o", "LogLevel=ERROR", "-o", "BatchMode=yes"}, "world.alpha@" + host
}

// writeTree fills dir as a server's folder looks: many small files and a few
// large ones, of noise that does not compress.
func writeTree(t *testing.T, dir string, small, large int, largeSize int64) {
	t.Helper()
	for i := 0; i < small+large; i++ {
		name := filepath.Join(dir, "mods", fmt.Sprintf("mod-%03d.jar", i))
		size := int64(1+i%50) << 12
		if i >= small {
			name = filepath.Join(dir, "world", "region", fmt.Sprintf("r.%d.0.mca", i-small))
			size = largeSize
		}
		if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
			t.Fatal(err)
		}
		f, err := os.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.Copy(f, newStream(byte(i), size)); err != nil {
			t.Fatal(err)
		}
		f.Close()
	}
}

// runFor runs a command, failing the test with what every goroutine of the
// gateway is doing if it takes longer than limit.
func runFor(t *testing.T, limit time.Duration, name string, args ...string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), limit)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	start := time.Now()
	err := cmd.Run()
	if ctx.Err() != nil {
		var stacks strings.Builder
		_ = pprof.Lookup("goroutine").WriteTo(&stacks, 2)
		t.Fatalf("%s %s stalled after %s: %s\n%s", name, strings.Join(args, " "), limit, out.String(), stacks.String())
	}
	if err != nil {
		t.Fatalf("%s %s: %v\n%s", name, strings.Join(args, " "), err, out.String())
	}
	t.Logf("%s took %s", name, time.Since(start).Round(time.Millisecond))
}

// rsync copies a server's folder in through the gateway and back out, as
// the user guide says it can, whole and in good time.
func TestGatewayRsyncWithOpenSSH(t *testing.T) {
	if testing.Short() {
		t.Skip("short")
	}
	if _, err := exec.LookPath("rsync"); err != nil {
		t.Skip("no rsync")
	}
	sshArgs, target := openSSH(t)
	large := int64(64 << 20)
	if mb, err := strconv.Atoi(os.Getenv("AVSYS_RSYNC_MB")); err == nil {
		large = int64(mb) << 20
	}
	dir := t.TempDir()
	src, there, back := filepath.Join(dir, "src"), filepath.Join(dir, "there"), filepath.Join(dir, "back")
	writeTree(t, src, 300, 3, large)
	e := strings.Join(sshArgs, " ")
	runFor(t, 3*time.Minute, "rsync", "-a", "-e", e, src+"/", target+":"+there+"/")
	runFor(t, 3*time.Minute, "rsync", "-a", "-e", e, target+":"+there+"/", back+"/")
	if out, err := exec.Command("diff", "-r", src, back).CombinedOutput(); err != nil {
		t.Fatalf("what came back differs: %v\n%s", err, out)
	}
}

// running says whether a process whose command line is exactly command is
// running.
func running(command string) bool {
	return exec.Command("pgrep", "-fx", command).Run() == nil
}

// Someone whose ssh goes away, killed or cut off, leaves nothing running in
// the workspace: their command is hung up on, as a terminal's is.
func TestGatewayHangsUpWithOpenSSH(t *testing.T) {
	if testing.Short() {
		t.Skip("short")
	}
	sshArgs, target := openSSH(t)
	command := "sleep 3001"
	t.Cleanup(func() { _ = exec.Command("pkill", "-fx", command).Run() })
	client := exec.Command(sshArgs[0], append(sshArgs[1:], target, command)...)
	if err := client.Start(); err != nil {
		t.Fatal(err)
	}
	for i := 0; !running(command); i++ {
		if i == 100 {
			t.Fatal("the command did not start")
		}
		time.Sleep(100 * time.Millisecond)
	}
	_ = client.Process.Kill()
	_ = client.Wait()
	for i := 0; running(command); i++ {
		if i == 100 {
			t.Fatal("the command was left running after its ssh was killed")
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// A command that exits but leaves something running behind it, which holds
// its output, ends its ssh with what it said, and does not keep it waiting.
func TestGatewayCommandThatLeavesSomethingRunningWithOpenSSH(t *testing.T) {
	if testing.Short() {
		t.Skip("short")
	}
	sshArgs, target := openSSH(t)
	t.Cleanup(func() { _ = exec.Command("pkill", "-fx", "sleep 3002").Run() })
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, sshArgs[0], append(sshArgs[1:], target, "sleep 3002 & echo started; exit 4")...).Output()
	if ctx.Err() != nil {
		t.Fatal("ssh did not end when its command did")
	}
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 4 || string(out) != "started\n" {
		t.Errorf("%q, %v", out, err)
	}
}
