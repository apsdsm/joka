package connection

import (
	"context"
	"errors"
	"net/url"
	"os"
	"testing"

	"github.com/apsdsm/joka/config"
	jokadb "github.com/apsdsm/joka/db"
	"github.com/apsdsm/joka/internal/providers"
)

// stubFetcher returns canned secret values without touching AWS.
type stubFetcher struct {
	values map[string]string
	err    error
}

func (s stubFetcher) Fetch(ctx context.Context, ref providers.SecretRef) (map[string]string, error) {
	return s.values, s.err
}

func TestResolve_Env(t *testing.T) {
	t.Run("returns DATABASE_URL for nil connection", func(t *testing.T) {
		t.Setenv("DATABASE_URL", "postgresql://root:pw@localhost:5432/db")
		dsn, err := Resolve(context.Background(), nil, nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if dsn != "postgresql://root:pw@localhost:5432/db" {
			t.Errorf("unexpected dsn: %q", dsn)
		}
	})

	t.Run("errors when DATABASE_URL is unset", func(t *testing.T) {
		os.Unsetenv("DATABASE_URL")
		if _, err := Resolve(context.Background(), &config.Connection{Source: "env"}, nil); err == nil {
			t.Fatal("expected error for missing DATABASE_URL")
		}
	})
}

func TestResolve_AWSAssembly(t *testing.T) {
	conn := &config.Connection{
		Source:   "aws_secrets_manager",
		Host:     "127.0.0.1",
		Port:     5432,
		User:     "root",
		Database: "lgc",
		Secret:   &config.Secret{SecretID: "lgc", PasswordKey: "pg_root_password"},
	}

	t.Run("assembles a dsn with the secret password", func(t *testing.T) {
		f := stubFetcher{values: map[string]string{"pg_root_password": "s3cr3t"}}
		dsn, err := Resolve(context.Background(), conn, f)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		want := "postgresql://root:s3cr3t@127.0.0.1:5432/lgc"
		if dsn != want {
			t.Errorf("got %q, want %q", dsn, want)
		}
	})

	t.Run("round-trips a password with special characters", func(t *testing.T) {
		f := stubFetcher{values: map[string]string{"pg_root_password": "p@ss:w/rd"}}
		dsn, err := Resolve(context.Background(), conn, f)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		// net/url must recover the exact password from the assembled DSN,
		// proving it is safe to hand to the driver verbatim.
		u, err := url.Parse(dsn)
		if err != nil {
			t.Fatalf("dsn did not parse back: %v (%q)", err, dsn)
		}
		got, _ := u.User.Password()
		if got != "p@ss:w/rd" {
			t.Errorf("password did not round-trip: got %q", got)
		}
	})

	t.Run("errors when the password key is missing", func(t *testing.T) {
		f := stubFetcher{values: map[string]string{"other": "x"}}
		if _, err := Resolve(context.Background(), conn, f); err == nil {
			t.Fatal("expected error for missing password key")
		}
	})

	t.Run("refuses a config that still asks for mysql", func(t *testing.T) {
		mysqlConn := *conn
		mysqlConn.Driver = "mysql"
		f := stubFetcher{values: map[string]string{"pg_root_password": "s3cr3t"}}

		_, err := Resolve(context.Background(), &mysqlConn, f)
		if !errors.Is(err, jokadb.ErrUnsupportedDriver) {
			t.Fatalf("expected ErrUnsupportedDriver, got: %v", err)
		}
	})
}
func TestResolve_AWSWholeURL(t *testing.T) {
	t.Run("uses the url_key value verbatim", func(t *testing.T) {
		conn := &config.Connection{
			Source: "aws_secrets_manager",
			Secret: &config.Secret{SecretID: "lgc/prd", URLKey: "database_url"},
		}
		f := stubFetcher{values: map[string]string{"database_url": "postgresql://root:pw@db:5432/lgc"}}
		dsn, err := Resolve(context.Background(), conn, f)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if dsn != "postgresql://root:pw@db:5432/lgc" {
			t.Errorf("unexpected dsn: %q", dsn)
		}
	})

	t.Run("uses a plain-string secret as the dsn", func(t *testing.T) {
		conn := &config.Connection{
			Source: "aws_secrets_manager",
			Secret: &config.Secret{SecretID: "lgc/prd"},
		}
		// parseSecretString keys a non-JSON secret under "".
		f := stubFetcher{values: parseSecretString("postgresql://u:p@h:5432/db")}
		dsn, err := Resolve(context.Background(), conn, f)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if dsn != "postgresql://u:p@h:5432/db" {
			t.Errorf("unexpected dsn: %q", dsn)
		}
	})
}

func TestResolve_Literal(t *testing.T) {
	t.Run("uses an inline url verbatim (source inferred)", func(t *testing.T) {
		conn := &config.Connection{URL: "postgresql://root:root@localhost:40204/lgc"}
		dsn, err := Resolve(context.Background(), conn, nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if dsn != "postgresql://root:root@localhost:40204/lgc" {
			t.Errorf("unexpected dsn: %q", dsn)
		}
	})

	t.Run("assembles from inline parts + password (source inferred)", func(t *testing.T) {
		conn := &config.Connection{
			Host: "localhost", Port: 40204,
			User: "root", Database: "lgc", Password: "root",
		}
		dsn, err := Resolve(context.Background(), conn, nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		want := "postgresql://root:root@localhost:40204/lgc"
		if dsn != want {
			t.Errorf("got %q, want %q", dsn, want)
		}
	})

	t.Run("explicit source: literal works too", func(t *testing.T) {
		conn := &config.Connection{Source: "literal", URL: "postgresql://u:p@h:5432/d"}
		dsn, err := Resolve(context.Background(), conn, nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if dsn != "postgresql://u:p@h:5432/d" {
			t.Errorf("unexpected dsn: %q", dsn)
		}
	})
}

func TestResolve_UnknownSource(t *testing.T) {
	if _, err := Resolve(context.Background(), &config.Connection{Source: "vault"}, nil); err == nil {
		t.Fatal("expected error for unknown source")
	}
}

func TestParseSecretString(t *testing.T) {
	t.Run("parses JSON object fields", func(t *testing.T) {
		m := parseSecretString(`{"pg_root_password":"abc","other":1}`)
		if m["pg_root_password"] != "abc" {
			t.Errorf("expected abc, got %q", m["pg_root_password"])
		}
		if m["other"] != "1" {
			t.Errorf("expected stringified 1, got %q", m["other"])
		}
	})

	t.Run("keys a non-JSON secret under empty string", func(t *testing.T) {
		m := parseSecretString("just-a-string")
		if m[""] != "just-a-string" {
			t.Errorf("expected raw value under \"\", got %v", m)
		}
	})
}

func TestResolve_RefusesMySQLBeforeFetching(t *testing.T) {
	// The driver check has to come before the secret fetch. Otherwise a config
	// still asking for MySQL fails as an AWS credentials error on the way to a
	// DSN that would have been refused anyway.
	conn := &config.Connection{
		Source:   "aws_secrets_manager",
		Driver:   "mysql",
		Host:     "127.0.0.1",
		User:     "root",
		Database: "lgc",
		Secret:   &config.Secret{SecretID: "lgc", PasswordKey: "pw"},
	}

	fetched := false
	f := recordingFetcher{onFetch: func() { fetched = true }}

	_, err := Resolve(context.Background(), conn, f)
	if !errors.Is(err, jokadb.ErrUnsupportedDriver) {
		t.Fatalf("expected ErrUnsupportedDriver, got: %v", err)
	}
	if fetched {
		t.Error("expected the secret never to be fetched")
	}
}

type recordingFetcher struct{ onFetch func() }

func (r recordingFetcher) Fetch(_ context.Context, _ providers.SecretRef) (map[string]string, error) {
	r.onFetch()
	return map[string]string{"pw": "x"}, nil
}
