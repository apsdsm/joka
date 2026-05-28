package connection

import (
	"context"
	"os"
	"testing"

	"github.com/apsdsm/joka/config"
	"github.com/go-sql-driver/mysql"
)

// stubFetcher returns canned secret values without touching AWS.
type stubFetcher struct {
	values map[string]string
	err    error
}

func (s stubFetcher) Fetch(ctx context.Context, secretID, region string) (map[string]string, error) {
	return s.values, s.err
}

func TestResolve_Env(t *testing.T) {
	t.Run("returns DATABASE_URL for nil connection", func(t *testing.T) {
		t.Setenv("DATABASE_URL", "root:pw@tcp(localhost:3306)/db")
		dsn, err := Resolve(context.Background(), nil, nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if dsn != "root:pw@tcp(localhost:3306)/db" {
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
		Driver:   "mysql",
		Host:     "127.0.0.1",
		Port:     3307,
		User:     "root",
		Database: "lgc",
		Secret:   &config.Secret{SecretID: "lgc", PasswordKey: "mysql_root_password"},
	}

	t.Run("assembles a mysql dsn with the secret password", func(t *testing.T) {
		f := stubFetcher{values: map[string]string{"mysql_root_password": "s3cr3t"}}
		dsn, err := Resolve(context.Background(), conn, f)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		want := "root:s3cr3t@tcp(127.0.0.1:3307)/lgc"
		if dsn != want {
			t.Errorf("got %q, want %q", dsn, want)
		}
	})

	t.Run("round-trips a password with special characters", func(t *testing.T) {
		f := stubFetcher{values: map[string]string{"mysql_root_password": "p@ss:w/rd"}}
		dsn, err := Resolve(context.Background(), conn, f)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		// go-sql-driver ParseDSN must recover the exact password from the DSN
		// (this is what db.Open re-parses), proving the assembled DSN is safe.
		cfg, err := mysql.ParseDSN(dsn)
		if err != nil {
			t.Fatalf("dsn did not parse back: %v (%q)", err, dsn)
		}
		if cfg.Passwd != "p@ss:w/rd" {
			t.Errorf("password did not round-trip: got %q", cfg.Passwd)
		}
	})

	t.Run("errors when the password key is missing", func(t *testing.T) {
		f := stubFetcher{values: map[string]string{"other": "x"}}
		if _, err := Resolve(context.Background(), conn, f); err == nil {
			t.Fatal("expected error for missing password key")
		}
	})
}

func TestResolve_AWSWholeURL(t *testing.T) {
	t.Run("uses the url_key value verbatim", func(t *testing.T) {
		conn := &config.Connection{
			Source: "aws_secrets_manager",
			Secret: &config.Secret{SecretID: "lgc/prd", URLKey: "database_url"},
		}
		f := stubFetcher{values: map[string]string{"database_url": "root:pw@tcp(db:3306)/lgc"}}
		dsn, err := Resolve(context.Background(), conn, f)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if dsn != "root:pw@tcp(db:3306)/lgc" {
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
		conn := &config.Connection{URL: "root:root@tcp(localhost:40204)/lgc"}
		dsn, err := Resolve(context.Background(), conn, nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if dsn != "root:root@tcp(localhost:40204)/lgc" {
			t.Errorf("unexpected dsn: %q", dsn)
		}
	})

	t.Run("assembles from inline parts + password (source inferred)", func(t *testing.T) {
		conn := &config.Connection{
			Driver: "mysql", Host: "localhost", Port: 40204,
			User: "root", Database: "lgc", Password: "root",
		}
		dsn, err := Resolve(context.Background(), conn, nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		want := "root:root@tcp(localhost:40204)/lgc"
		if dsn != want {
			t.Errorf("got %q, want %q", dsn, want)
		}
	})

	t.Run("explicit source: literal works too", func(t *testing.T) {
		conn := &config.Connection{Source: "literal", URL: "u:p@tcp(h:3306)/d"}
		dsn, err := Resolve(context.Background(), conn, nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if dsn != "u:p@tcp(h:3306)/d" {
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
		m := parseSecretString(`{"mysql_root_password":"abc","other":1}`)
		if m["mysql_root_password"] != "abc" {
			t.Errorf("expected abc, got %q", m["mysql_root_password"])
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
