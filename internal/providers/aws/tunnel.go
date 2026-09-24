package aws

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os/exec"
	"strconv"
	"sync"
	"time"

	"github.com/apsdsm/joka/internal/providers"
)

// SessionManager forwards a local port to a database in a private subnet
// through AWS Systems Manager.
//
// It shells out to the `aws` CLI, which is a line joka otherwise holds — pg_dump
// is the only other binary it runs. The alternative is reimplementing the
// Session Manager websocket protocol, which is what the session-manager-plugin
// binary exists to do and is not something a migration tool should carry. The
// cost is named rather than hidden: joka checks for both binaries up front and
// says which one is missing.
type SessionManager struct {
	// run builds the command. Replaced in tests, which is what makes the
	// lifecycle — readiness, teardown, a process that dies early — testable
	// without an AWS account.
	run func(ctx context.Context, name string, args ...string) *exec.Cmd
	// lookPath reports whether a binary is on PATH. Replaced in tests.
	lookPath func(string) (string, error)
	// ready polls until something is listening. Replaced in tests.
	ready func(ctx context.Context, address string, within time.Duration) error
}

// Required binaries. The plugin is not a normal package on most systems, so
// naming it separately is worth doing: "aws: command not found" sends people
// to the wrong place when it is the plugin that is missing.
const (
	awsBinary    = "aws"
	pluginBinary = "session-manager-plugin"
)

// ErrMissingBinary means a binary the tunnel needs is not on PATH.
var ErrMissingBinary = errors.New("a binary the ssm tunnel needs is not installed")

// Open starts the port forward and returns once something is listening on the
// local end.
//
// Returning early would hand back a port nothing answers on, and the caller's
// next act is to connect to it — so the readiness wait belongs here rather than
// being every caller's problem, which is the same reason db.OpenWait exists.
func (s SessionManager) Open(ctx context.Context, spec providers.TunnelSpec) (*providers.Session, error) {
	s = s.withDefaults()

	for _, bin := range []string{awsBinary, pluginBinary} {
		if _, err := s.lookPath(bin); err != nil {
			return nil, fmt.Errorf("%w: %s (needed to reach %s through %s)",
				ErrMissingBinary, bin, spec.RemoteHost, spec.Target)
		}
	}

	if spec.Target == "" || spec.RemoteHost == "" || spec.RemotePort == 0 {
		return nil, fmt.Errorf("an ssm tunnel needs target, remote_host and remote_port")
	}

	localPort := spec.LocalPort
	if localPort == 0 {
		free, err := freePort()
		if err != nil {
			return nil, fmt.Errorf("choosing a local port: %w", err)
		}
		localPort = free
	}

	// The session is killed by cancelling this, not by signalling the process
	// group: the CLI spawns the plugin as a child, and killing only the parent
	// leaves the plugin holding the port.
	runCtx, cancel := context.WithCancel(ctx)

	cmd := s.run(runCtx, awsBinary,
		"ssm", "start-session",
		"--target", spec.Target,
		"--document-name", "AWS-StartPortForwardingSessionToRemoteHost",
		"--parameters", parameters(spec.RemoteHost, spec.RemotePort, localPort),
	)

	if err := cmd.Start(); err != nil {
		cancel()
		return nil, fmt.Errorf("starting the ssm session: %w", err)
	}

	// Reaped in the background so a session that dies on its own does not
	// become a zombie, and so Close does not block on a process that has
	// already gone.
	exited := make(chan error, 1)
	go func() {
		exited <- cmd.Wait()
		// Closed as well as sent on, so a second receive returns immediately.
		// The early-exit check below takes the value, and the closer receives
		// again — on an open channel that second receive would block for ever.
		close(exited)
	}()

	var once sync.Once
	closer := func() error {
		once.Do(func() {
			cancel()
			<-exited
		})
		return nil
	}

	address := net.JoinHostPort("127.0.0.1", strconv.Itoa(localPort))

	if err := s.ready(runCtx, address, tunnelTimeout); err != nil {
		// Look for the session's own failure before closing, not after: the
		// closer waits on this same channel and would consume the exit error.
		// It also has to be before the cancel, or every failure reads as
		// "signal: killed" — which is joka killing it, not why it went.
		//
		// A session that failed to start usually failed for a reason the CLI
		// printed, and that is the useful part: it says "instance not found"
		// or "not authorized", where this can only say it never came up.
		var runErr error
		select {
		case runErr = <-exited:
		case <-time.After(earlyExitGrace):
		}

		closer()

		if runErr != nil {
			return nil, fmt.Errorf("the ssm session ended before the tunnel opened: %w", runErr)
		}

		return nil, fmt.Errorf("the ssm tunnel to %s:%d did not open: %w", spec.RemoteHost, spec.RemotePort, err)
	}

	return &providers.Session{LocalHost: "127.0.0.1", LocalPort: localPort, Close: closer}, nil
}

// earlyExitGrace is how long to give a failing session to report its own exit
// before joka gives up guessing. A session that fails does so quickly, and a
// plain non-blocking check races it — the process has been started but has not
// been reaped yet, so the useful error is missed exactly when it exists.
const earlyExitGrace = 250 * time.Millisecond

// tunnelTimeout is how long to wait for the forward to come up. A session
// normally opens in a second or two; thirty allows for a slow SSO refresh.
const tunnelTimeout = 30 * time.Second

func (s SessionManager) withDefaults() SessionManager {
	if s.run == nil {
		s.run = exec.CommandContext
	}
	if s.lookPath == nil {
		s.lookPath = exec.LookPath
	}
	if s.ready == nil {
		s.ready = waitForListener
	}

	return s
}

// parameters renders the document parameters as the CLI's shorthand JSON.
func parameters(host string, remote, local int) string {
	return fmt.Sprintf(`{"host":["%s"],"portNumber":["%d"],"localPortNumber":["%d"]}`,
		host, remote, local)
}

// freePort asks the kernel for an unused port by binding one and letting go.
//
// There is a race between letting go and the session claiming it, which is
// unavoidable without the session accepting a listener. It is small, and the
// alternative — a fixed port in the config — collides with whatever else the
// developer is running, every time, rather than rarely.
func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()

	return l.Addr().(*net.TCPAddr).Port, nil
}

// waitForListener polls until something accepts a connection on address.
func waitForListener(ctx context.Context, address string, within time.Duration) error {
	deadline := time.Now().Add(within)

	for {
		conn, err := net.DialTimeout("tcp", address, 500*time.Millisecond)
		if err == nil {
			conn.Close()
			return nil
		}

		if ctx.Err() != nil {
			return ctx.Err()
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("nothing listening on %s after %s", address, within)
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
}
