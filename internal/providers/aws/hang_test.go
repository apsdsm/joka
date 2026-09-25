//go:build unix

package aws

import (
	"context"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/apsdsm/joka/internal/providers"
)

// TestCloseDoesNotHangOnASurvivingGrandchild reproduces what onc saw: joka
// finished its work and then sat there.
//
// `aws ssm start-session` spawns session-manager-plugin, which inherits the
// pipe joka captures stderr through. Killing the CLI leaves the plugin holding
// the write end, and cmd.Wait blocks until every holder closes it — so Close
// never returned. Capturing stderr is what introduced it; before that, stderr
// was an inherited descriptor and exec made no pipe to wait on.
func TestCloseDoesNotHangOnASurvivingGrandchild(t *testing.T) {
	l := listenOn(t, 0)
	port := l.Addr().(*net.TCPAddr).Port

	// The grandchild touches this before it settles, so the test closes the
	// session only once one exists. Without the marker the test races the
	// shell and passes whenever Close wins.
	marker := filepath.Join(t.TempDir(), "grandchild")
	script := "( sleep 60 & echo $! > " + marker + "; wait ) & sleep 60"

	s := SessionManager{
		lookPath: present,
		run: func(ctx context.Context, name string, args ...string) *exec.Cmd {
			return exec.CommandContext(ctx, "sh", "-c", script)
		},
	}

	sess, err := s.Open(context.Background(), providers.TunnelSpec{
		Target: "i-1", RemoteHost: "h", RemotePort: 5432, LocalPort: port,
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	waitForFile(t, marker)

	done := make(chan error, 1)
	go func() { done <- sess.Close() }()

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Close: %v", err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("Close hung: joka would sit there after its work was done")
	}

	// Returning promptly is half of it. The other half is that the grandchild
	// is gone: an orphaned session-manager-plugin holds the local port, and the
	// next run cannot bind it.
	assertGone(t, readPID(t, marker))
}

func readPID(t *testing.T, path string) int {
	t.Helper()

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading the grandchild pid: %v", err)
	}

	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		t.Fatalf("parsing the grandchild pid %q: %v", raw, err)
	}

	return pid
}

// assertGone reports whether a process has died, allowing a moment for the
// signal to land.
func assertGone(t *testing.T, pid int) {
	t.Helper()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		// Signal 0 tests for existence without sending anything.
		if err := syscall.Kill(pid, 0); err != nil {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}

	syscall.Kill(pid, syscall.SIGKILL)
	t.Errorf("the grandchild (pid %d) outlived the tunnel, holding the local port", pid)
}

func waitForFile(t *testing.T, path string) {
	t.Helper()

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}

	t.Fatalf("the grandchild never appeared at %s", path)
}
