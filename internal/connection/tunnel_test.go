package connection

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/apsdsm/joka/config"
	"github.com/apsdsm/joka/internal/providers"
)

type fakeTunnel struct {
	got    providers.TunnelSpec
	closed int
	err    error
}

func (f *fakeTunnel) Open(_ context.Context, spec providers.TunnelSpec) (*providers.Session, error) {
	if f.err != nil {
		return nil, f.err
	}
	f.got = spec

	return &providers.Session{
		LocalHost: "127.0.0.1",
		LocalPort: 54321,
		Close:     func() error { f.closed++; return nil },
	}, nil
}

func TestResolveWithTunnel(t *testing.T) {
	ctx := context.Background()

	t.Run("no tunnel returns a closer that does nothing", func(t *testing.T) {
		// So no caller branches on whether one was declared.
		t.Setenv("DATABASE_URL", "postgresql://u:p@db:5432/app")

		dsn, closer, err := ResolveWithTunnel(ctx, nil, nil)
		if err != nil {
			t.Fatalf("ResolveWithTunnel: %v", err)
		}
		if dsn != "postgresql://u:p@db:5432/app" {
			t.Errorf("expected the DSN untouched, got %q", dsn)
		}
		if closer == nil {
			t.Fatal("expected a non-nil closer")
		}
		if err := closer(); err != nil {
			t.Errorf("expected the no-op closer to succeed, got: %v", err)
		}
	})

	t.Run("a literal URL is redirected at the local end", func(t *testing.T) {
		// The rewrite is on the finished DSN, because the host is inside it.
		fake := &fakeTunnel{}
		providers.RegisterTunnel("faketun", fake)

		conn := &config.Connection{
			Source: "literal",
			URL:    "postgresql://u:p@db.private:5432/app?sslmode=require",
			Tunnel: &config.Tunnel{Provider: "faketun", Target: "i-1", RemoteHost: "db.private", RemotePort: 5432},
		}

		dsn, closer, err := ResolveWithTunnel(ctx, conn, nil)
		if err != nil {
			t.Fatalf("ResolveWithTunnel: %v", err)
		}
		defer closer()

		if !strings.Contains(dsn, "127.0.0.1:54321") {
			t.Errorf("expected the DSN to point at the tunnel, got %q", dsn)
		}
		// Everything else has to survive, or the redirect would silently drop
		// credentials or TLS settings.
		for _, want := range []string{"u:p@", "/app", "sslmode=require"} {
			if !strings.Contains(dsn, want) {
				t.Errorf("expected %q to survive the rewrite, got %q", want, dsn)
			}
		}
	})

	t.Run("the database is named once", func(t *testing.T) {
		// remote_host and remote_port default to the connection's own, because
		// repeating them is how the two come to disagree — and connecting to
		// the wrong database through a working tunnel fails silently.
		fake := &fakeTunnel{}
		providers.RegisterTunnel("faketun2", fake)

		conn := &config.Connection{
			Source: "literal", Host: "db.private", Port: 6543, User: "u", Database: "app",
			Tunnel: &config.Tunnel{Provider: "faketun2", Target: "i-2"},
		}

		_, closer, err := ResolveWithTunnel(ctx, conn, nil)
		if err != nil {
			t.Fatalf("ResolveWithTunnel: %v", err)
		}
		defer closer()

		if fake.got.RemoteHost != "db.private" || fake.got.RemotePort != 6543 {
			t.Errorf("expected the connection's own host and port, got %s:%d",
				fake.got.RemoteHost, fake.got.RemotePort)
		}
	})

	t.Run("a tunnel that will not open is reported, and closed", func(t *testing.T) {
		providers.RegisterTunnel("badtun", &fakeTunnel{err: errors.New("no such instance")})

		conn := &config.Connection{
			Source: "literal", URL: "postgresql://u:p@h:5432/app",
			Tunnel: &config.Tunnel{Provider: "badtun", Target: "i-3", RemoteHost: "h", RemotePort: 5432},
		}

		_, closer, err := ResolveWithTunnel(ctx, conn, nil)
		if err == nil {
			t.Fatal("expected the failure to be reported")
		}
		if closer == nil {
			t.Fatal("expected a closer even on failure")
		}
		if !strings.Contains(err.Error(), "no such instance") {
			t.Errorf("expected the provider's reason, got: %v", err)
		}
	})

	t.Run("an unknown tunnel provider names the ones there are", func(t *testing.T) {
		conn := &config.Connection{
			Source: "literal", URL: "postgresql://u:p@h:5432/app",
			Tunnel: &config.Tunnel{Provider: "azure", Target: "x", RemoteHost: "h", RemotePort: 5432},
		}

		if _, _, err := ResolveWithTunnel(ctx, conn, nil); err == nil {
			t.Fatal("expected an unknown provider to be refused")
		}
	})

	t.Run("a DSN that fails to resolve closes the tunnel", func(t *testing.T) {
		// Otherwise a config error leaks a child process and a port.
		fake := &fakeTunnel{}
		providers.RegisterTunnel("faketun3", fake)

		conn := &config.Connection{
			Source: "secret",
			Tunnel: &config.Tunnel{Provider: "faketun3", Target: "i-4", RemoteHost: "h", RemotePort: 5432},
		}

		if _, _, err := ResolveWithTunnel(ctx, conn, nil); err == nil {
			t.Fatal("expected a secret source with no secret_id to fail")
		}
		if fake.closed != 1 {
			t.Errorf("expected the tunnel to be closed once, got %d", fake.closed)
		}
	})
}
