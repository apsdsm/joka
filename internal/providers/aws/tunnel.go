package aws

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os/exec"
	"strconv"
	"strings"
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
	// lookup finds an instance by tag. Replaced in tests, which is what makes
	// target resolution testable without an AWS account.
	lookup instanceLookup
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

	if spec.RemoteHost == "" || spec.RemotePort == 0 {
		return nil, fmt.Errorf("an ssm tunnel needs remote_host and remote_port")
	}

	// Resolved before anything is started. A selector that matches nothing is
	// a configuration error, and reporting it as one beats starting a session
	// against an empty target and reading whatever the CLI makes of that.
	target, err := findInstance(ctx, s.lookup, spec)
	if err != nil {
		return nil, err
	}
	spec.Target = target

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

	cmd := s.run(runCtx, awsBinary, arguments(spec, localPort)...)

	// The CLI says why it failed — "instance not found", "not authorized", an
	// expired SSO session — and without capturing it that sentence went to a
	// pipe nobody read, leaving joka to report only that nothing came up.
	var stderr lastLines
	cmd.Stderr = &stderr

	// Capturing stderr is what made teardown able to hang. A non-*os.File
	// writer makes exec create a pipe, and Wait blocks until every process
	// holding the write end closes it — including session-manager-plugin,
	// which outlives the CLI joka kills. contain kills the whole group so the
	// plugin goes too; WaitDelay is the backstop that bounds the wait whatever
	// escapes, because a tool that has finished its work must exit.
	contain(cmd)
	cmd.WaitDelay = teardownGrace

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

	// Whichever happens first wins. Waiting on readiness alone meant a session
	// that died in the first second still took the full thirty to be reported,
	// because nothing was watching the process while the poll ran.
	readyErr := make(chan error, 1)
	go func() { readyErr <- s.ready(runCtx, address, tunnelTimeout) }()

	var (
		runErr   error
		readyRes error
		died     bool
	)

	select {
	case readyRes = <-readyErr:
		if readyRes == nil {
			return &providers.Session{LocalHost: "127.0.0.1", LocalPort: localPort, Close: closer}, nil
		}
		// The poll gave up on its own. Give the process a moment to report an
		// exit of its own, which is the better error when there is one.
		select {
		case runErr = <-exited:
			died = true
		case <-time.After(earlyExitGrace):
		}
	case runErr = <-exited:
		died = true
	}

	// Closing must come after reading `exited`: the closer receives on the same
	// channel, and cancelling first would make every failure read as
	// "signal: killed" — joka killing it, not why it went.
	closer()

	if died {
		return nil, fmt.Errorf("the ssm session ended before the tunnel opened%s: %w",
			stderr.suffix(), orExitedCleanly(runErr))
	}

	return nil, fmt.Errorf("the ssm tunnel to %s:%d did not open%s: %w",
		spec.RemoteHost, spec.RemotePort, stderr.suffix(), readyRes)
}

// teardownGrace bounds Wait after the session is killed: how long to allow the
// process to die and its pipes to be released before joka stops waiting and
// closes them itself. Two seconds is far longer than a SIGKILL needs and short
// enough that nobody watches it.
const teardownGrace = 2 * time.Second

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
	if s.lookup == nil {
		s.lookup = describeInstances
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

// arguments builds the CLI invocation.
//
// region and profile travel in Params, because they are AWS's and the
// provider-neutral TunnelSpec must not grow a field per vendor. They parsed and
// went nowhere before this: a config naming a profile was accepted in full and
// then the session was opened against the default one.
func arguments(spec providers.TunnelSpec, localPort int) []string {
	args := []string{"ssm", "start-session",
		"--target", spec.Target,
		"--document-name", "AWS-StartPortForwardingSessionToRemoteHost",
		"--parameters", parameters(spec.RemoteHost, spec.RemotePort, localPort),
	}

	if region := spec.Params[ParamRegion]; region != "" {
		args = append(args, "--region", region)
	}
	if profile := spec.Params[ParamProfile]; profile != "" {
		args = append(args, "--profile", profile)
	}

	return args
}

// orExitedCleanly names the case a wait error cannot: a session that ended of
// its own accord with status zero, which is still a session that is not there.
func orExitedCleanly(err error) error {
	if err != nil {
		return err
	}

	return errors.New("it exited without an error")
}

// lastLines keeps the tail of a stream, for putting a subprocess's own
// complaint into joka's error.
//
// Bounded because it holds whatever the CLI decides to print, and an error
// message is not the place for a screen of it. The tail rather than the head:
// the reason a command failed is what it said last.
type lastLines struct {
	mu  sync.Mutex
	buf []byte
}

// stderrKept is how much of a subprocess's output to carry into an error.
const stderrKept = 2000

func (l *lastLines) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.buf = append(l.buf, p...)
	if len(l.buf) > stderrKept {
		l.buf = l.buf[len(l.buf)-stderrKept:]
	}

	return len(p), nil
}

// suffix renders what was captured as a clause, or nothing at all.
func (l *lastLines) suffix() string {
	l.mu.Lock()
	defer l.mu.Unlock()

	s := strings.TrimSpace(string(l.buf))
	if s == "" {
		return ""
	}

	return " (aws said: " + strings.ReplaceAll(s, "\n", "; ") + ")"
}
