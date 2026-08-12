package infra

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/go-sql-driver/mysql"

	"github.com/apsdsm/joka/internal/domains/migration/domain"
)

func TestStripPsqlMetaCommands(t *testing.T) {
	// Recent pg_dump wraps its output in \restrict / \unrestrict. They are psql
	// client directives, and the server rejects them.
	dump := "--\n-- PostgreSQL database dump\n--\n\n" +
		"\\restrict hpWgMp36v5WRfVa7ZLPlgo\n\n" +
		"SET statement_timeout = 0;\n" +
		"CREATE TABLE public.t (id integer NOT NULL);\n\n" +
		"\\unrestrict hpWgMp36v5WRfVa7ZLPlgo\n"

	got := stripPsqlMetaCommands(dump)

	if strings.Contains(got, `\restrict`) || strings.Contains(got, `\unrestrict`) {
		t.Errorf("expected meta-commands to be removed, got:\n%s", got)
	}
	if !strings.Contains(got, "CREATE TABLE public.t") {
		t.Errorf("expected SQL to survive, got:\n%s", got)
	}
	if !strings.Contains(got, "SET statement_timeout = 0;") {
		t.Errorf("expected the SET preamble to survive (it is valid SQL), got:\n%s", got)
	}
}

func TestStripMySQLConditionalStatements(t *testing.T) {
	t.Run("it removes single-line conditional statements", func(t *testing.T) {
		// Real mysqldump output shape: charset save/restore around each table.
		dump := "/*!40101 SET @saved_cs_client     = @@character_set_client */;\n" +
			"/*!50503 SET character_set_client = utf8mb4 */;\n" +
			"CREATE TABLE `t` (\n  `id` int NOT NULL,\n  PRIMARY KEY (`id`)\n) ENGINE=InnoDB;\n" +
			"/*!40101 SET character_set_client = @saved_cs_client */;\n"

		got, err := stripMySQLConditionalStatements(dump)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if strings.Contains(got, "/*!") {
			t.Errorf("expected conditional statements to be removed, got:\n%s", got)
		}
		if !strings.Contains(got, "CREATE TABLE `t`") {
			t.Errorf("expected the table to survive, got:\n%s", got)
		}
	})

	t.Run("it refuses a multi-line conditional block", func(t *testing.T) {
		// This is what a view looks like in mysqldump output. Stripping it
		// line-by-line would leave dangling SQL, so it must be refused instead.
		dump := "/*!50001 CREATE VIEW `v` AS SELECT\n 1 AS `id`*/;\n"

		_, err := stripMySQLConditionalStatements(dump)
		if !errors.Is(err, domain.ErrDumpNotApplicable) {
			t.Fatalf("expected ErrDumpNotApplicable, got: %v", err)
		}
	})
}

func TestMysqldumpConnArgs(t *testing.T) {
	t.Run("it maps a tcp DSN to host and port", func(t *testing.T) {
		cfg, err := mysql.ParseDSN("user:pass@tcp(db.internal:3307)/appdb")
		if err != nil {
			t.Fatalf("ParseDSN: %v", err)
		}

		args, err := mysqldumpConnArgs(cfg)
		if err != nil {
			t.Fatalf("mysqldumpConnArgs: %v", err)
		}

		joined := strings.Join(args, " ")
		for _, want := range []string{"--protocol=TCP", "--host=db.internal", "--port=3307", "--user=user"} {
			if !strings.Contains(joined, want) {
				t.Errorf("expected %q in %v", want, args)
			}
		}
		if strings.Contains(joined, "pass") {
			t.Errorf("password must not appear in the argument list, got %v", args)
		}
	})

	t.Run("it maps a unix socket DSN", func(t *testing.T) {
		cfg, err := mysql.ParseDSN("user@unix(/var/run/mysqld/mysqld.sock)/appdb")
		if err != nil {
			t.Fatalf("ParseDSN: %v", err)
		}

		args, err := mysqldumpConnArgs(cfg)
		if err != nil {
			t.Fatalf("mysqldumpConnArgs: %v", err)
		}

		joined := strings.Join(args, " ")
		if !strings.Contains(joined, "--protocol=SOCKET") || !strings.Contains(joined, "--socket=/var/run/mysqld/mysqld.sock") {
			t.Errorf("expected socket flags, got %v", args)
		}
	})

	t.Run("it maps TLS modes and refuses ones it cannot express", func(t *testing.T) {
		cases := map[string]string{
			"skip-verify": "--ssl-mode=REQUIRED",
			"true":        "--ssl-mode=VERIFY_IDENTITY",
			"preferred":   "--ssl-mode=PREFERRED",
		}
		for tlsConfig, want := range cases {
			cfg, err := mysql.ParseDSN("user:pass@tcp(h:3306)/db?tls=" + tlsConfig)
			if err != nil {
				t.Fatalf("ParseDSN(%s): %v", tlsConfig, err)
			}
			args, err := mysqldumpConnArgs(cfg)
			if err != nil {
				t.Fatalf("mysqldumpConnArgs(%s): %v", tlsConfig, err)
			}
			if !strings.Contains(strings.Join(args, " "), want) {
				t.Errorf("tls=%s: expected %q, got %v", tlsConfig, want, args)
			}
		}

		cfg, err := mysql.ParseDSN("user:pass@tcp(h:3306)/db")
		if err != nil {
			t.Fatalf("ParseDSN: %v", err)
		}
		cfg.TLSConfig = "my-custom-config"
		if _, err := mysqldumpConnArgs(cfg); err == nil {
			t.Error("expected an error for a TLS config that cannot be translated")
		}
	})
}

func TestRunDumpToolMissingBinary(t *testing.T) {
	// The most likely first-run failure now that joka depends on an external
	// binary, so the error has to name it.
	_, err := runDumpTool(context.Background(), "joka-no-such-dump-tool", nil, nil)
	if !errors.Is(err, domain.ErrDumpToolMissing) {
		t.Fatalf("expected ErrDumpToolMissing, got: %v", err)
	}
	if !strings.Contains(err.Error(), "joka-no-such-dump-tool") {
		t.Errorf("expected the error to name the tool, got: %v", err)
	}
}
