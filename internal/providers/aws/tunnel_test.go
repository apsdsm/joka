package aws

import (
	"context"
	"errors"
	"net"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/apsdsm/joka/internal/providers"
)

// present is a lookPath that finds everything.
func present(name string) (string, error) { return "/usr/bin/" + name, nil }

// listenOn starts a real listener, which is what the readiness wait is
// actually looking for — so the lifecycle is testable without an AWS account.
func listenOn(t *testing.T, port int) net.Listener {
	t.Helper()

	l, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		t.Fatalf("listening: %v", err)
	}
	t.Cleanup(func() { l.Close() })

	return l
}

func spec() providers.TunnelSpec {
	return providers.TunnelSpec{Target: "i-123", RemoteHost: "db.internal", RemotePort: 5432}
}

func TestSessionManagerOpen(t *testing.T) {
	ctx := context.Background()

	t.Run("a missing binary is named", func(t *testing.T) {
		// "aws: command not found" sends people to the wrong place when it is
		// the plugin that is missing, so each is checked and named.
		s := SessionManager{
			lookPath: func(name string) (string, error) {
				if name == pluginBinary {
					return "", errors.New("not found")
				}
				return "/usr/bin/" + name, nil
			},
		}

		_, err := s.Open(ctx, spec())
		if !errors.Is(err, ErrMissingBinary) {
			t.Fatalf("expected ErrMissingBinary, got: %v", err)
		}
		if !strings.Contains(err.Error(), pluginBinary) {
			t.Errorf("expected the plugin to be named, got: %v", err)
		}
	})

	t.Run("an incomplete spec is refused before anything is started", func(t *testing.T) {
		started := false
		s := SessionManager{
			lookPath: present,
			run: func(ctx context.Context, name string, args ...string) *exec.Cmd {
				started = true
				return exec.CommandContext(ctx, "true")
			},
		}

		if _, err := s.Open(ctx, providers.TunnelSpec{Target: "i-123"}); err == nil {
			t.Fatal("expected a spec with no remote host to be refused")
		}
		if started {
			t.Error("expected nothing to be started for an incomplete spec")
		}
	})

	t.Run("it passes the document parameters the forward needs", func(t *testing.T) {
		var got []string
		l := listenOn(t, 0)
		port := l.Addr().(*net.TCPAddr).Port

		s := SessionManager{
			lookPath: present,
			run: func(ctx context.Context, name string, args ...string) *exec.Cmd {
				got = append([]string{name}, args...)
				return exec.CommandContext(ctx, "sleep", "30")
			},
		}

		sess, err := s.Open(ctx, providers.TunnelSpec{
			Target: "i-abc", RemoteHost: "db.internal", RemotePort: 5432, LocalPort: port,
		})
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		defer sess.Close()

		joined := strings.Join(got, " ")
		for _, want := range []string{
			"aws", "ssm start-session", "--target i-abc",
			"AWS-StartPortForwardingSessionToRemoteHost",
			`"host":["db.internal"]`, `"portNumber":["5432"]`,
			`"localPortNumber":["` + strconv.Itoa(port) + `"]`,
		} {
			if !strings.Contains(joined, want) {
				t.Errorf("expected the command to contain %q, got: %s", want, joined)
			}
		}
	})

	t.Run("it returns the local end once something is listening", func(t *testing.T) {
		l := listenOn(t, 0)
		port := l.Addr().(*net.TCPAddr).Port

		s := SessionManager{
			lookPath: present,
			run: func(ctx context.Context, name string, args ...string) *exec.Cmd {
				return exec.CommandContext(ctx, "sleep", "30")
			},
		}

		sess, err := s.Open(ctx, providers.TunnelSpec{
			Target: "i-1", RemoteHost: "h", RemotePort: 5432, LocalPort: port,
		})
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		defer sess.Close()

		if sess.LocalPort != port || sess.LocalHost != "127.0.0.1" {
			t.Errorf("expected the local end, got %s:%d", sess.LocalHost, sess.LocalPort)
		}
	})

	t.Run("a session that never opens is an error, not a hang", func(t *testing.T) {
		// Returning early would hand back a port nothing answers on, and the
		// caller's next act is to connect to it.
		s := SessionManager{
			lookPath: present,
			run: func(ctx context.Context, name string, args ...string) *exec.Cmd {
				return exec.CommandContext(ctx, "sleep", "30")
			},
			ready: func(context.Context, string, time.Duration) error {
				return errors.New("nothing listening")
			},
		}

		if _, err := s.Open(ctx, spec()); err == nil {
			t.Fatal("expected an error when the tunnel never opens")
		}
	})

	t.Run("a session that dies early reports its own failure", func(t *testing.T) {
		// The CLI prints why — "instance not found", "not authorized" — and
		// that is the useful part. This can only say it never came up.
		s := SessionManager{
			lookPath: present,
			run: func(ctx context.Context, name string, args ...string) *exec.Cmd {
				return exec.CommandContext(ctx, "false")
			},
			ready: func(context.Context, string, time.Duration) error {
				return errors.New("nothing listening")
			},
		}

		_, err := s.Open(ctx, spec())
		if err == nil || !strings.Contains(err.Error(), "ended before the tunnel opened") {
			t.Fatalf("expected the process failure to be reported, got: %v", err)
		}
	})

	t.Run("Close is safe twice", func(t *testing.T) {
		// It runs from a defer on a path that may already have failed.
		l := listenOn(t, 0)
		port := l.Addr().(*net.TCPAddr).Port

		s := SessionManager{
			lookPath: present,
			run: func(ctx context.Context, name string, args ...string) *exec.Cmd {
				return exec.CommandContext(ctx, "sleep", "30")
			},
		}

		sess, err := s.Open(ctx, providers.TunnelSpec{
			Target: "i-1", RemoteHost: "h", RemotePort: 5432, LocalPort: port,
		})
		if err != nil {
			t.Fatalf("Open: %v", err)
		}

		if err := sess.Close(); err != nil {
			t.Errorf("first Close: %v", err)
		}
		if err := sess.Close(); err != nil {
			t.Errorf("second Close: %v", err)
		}
	})
}

func TestFreePort(t *testing.T) {
	port, err := freePort()
	if err != nil {
		t.Fatalf("freePort: %v", err)
	}
	if port <= 0 {
		t.Errorf("expected a usable port, got %d", port)
	}
}

func TestParseSecretString(t *testing.T) {
	t.Run("a JSON object yields its fields", func(t *testing.T) {
		got := ParseSecretString(`{"user":"joka","port":5432,"tls":true}`)
		if got["user"] != "joka" || got["port"] != "5432" || got["tls"] != "true" {
			t.Errorf("unexpected values: %v", got)
		}
	})

	t.Run("a plain string yields one unnamed entry", func(t *testing.T) {
		// Whole-URL mode reads this entry, so the convention is joka's and
		// every provider owes the same shape.
		got := ParseSecretString("postgresql://u:p@h:5432/db")
		if got[""] != "postgresql://u:p@h:5432/db" {
			t.Errorf("unexpected values: %v", got)
		}
	})
}

func TestTunnelParamsReachTheCLI(t *testing.T) {
	// region and profile parsed and went nowhere: a config naming a profile
	// was accepted in full and the session opened against the default one.
	l := listenOn(t, 0)
	port := l.Addr().(*net.TCPAddr).Port

	var got []string
	s := SessionManager{
		lookPath: present,
		run: func(ctx context.Context, name string, args ...string) *exec.Cmd {
			got = args
			return exec.CommandContext(ctx, "sleep", "30")
		},
	}

	sess, err := s.Open(context.Background(), providers.TunnelSpec{
		Target: "i-1", RemoteHost: "h", RemotePort: 5432, LocalPort: port,
		Params: map[string]string{ParamRegion: "ap-northeast-1", ParamProfile: "prod"},
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer sess.Close()

	joined := strings.Join(got, " ")
	for _, want := range []string{"--region ap-northeast-1", "--profile prod"} {
		if !strings.Contains(joined, want) {
			t.Errorf("expected %q in the invocation, got: %s", want, joined)
		}
	}
}

func TestSessionThatDiesIsReportedAtOnce(t *testing.T) {
	// It used to take the full readiness timeout, because nothing watched the
	// process while the poll ran.
	s := SessionManager{
		lookPath: present,
		run: func(ctx context.Context, name string, args ...string) *exec.Cmd {
			cmd := exec.CommandContext(ctx, "sh", "-c", "echo 'An error occurred: TargetNotConnected' >&2; exit 254")
			return cmd
		},
	}

	start := time.Now()
	_, err := s.Open(context.Background(), spec())
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected an error")
	}
	if elapsed > 5*time.Second {
		t.Errorf("expected the death to be reported at once, took %s", elapsed)
	}
	if !strings.Contains(err.Error(), "ended before the tunnel opened") {
		t.Errorf("expected the early exit to be named, got: %v", err)
	}
	// The CLI's own sentence is the useful part.
	if !strings.Contains(err.Error(), "TargetNotConnected") {
		t.Errorf("expected the aws message to be carried, got: %v", err)
	}
}

func TestSessionThatExitsCleanlyIsStillAFailure(t *testing.T) {
	s := SessionManager{
		lookPath: present,
		run: func(ctx context.Context, name string, args ...string) *exec.Cmd {
			return exec.CommandContext(ctx, "true")
		},
	}

	_, err := s.Open(context.Background(), spec())
	if err == nil {
		t.Fatal("expected a session that ended to be an error even at status zero")
	}
	if !strings.Contains(err.Error(), "exited without an error") {
		t.Errorf("expected the clean exit to be named, got: %v", err)
	}
}
