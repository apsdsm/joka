package infra

import (
	"context"
	"errors"
	"strings"
	"testing"

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

func TestNewSchemaDumper(t *testing.T) {
	dumper := NewSchemaDumper(nil, "postgres://localhost/db")
	if dumper.Tool() != "pg_dump" {
		t.Errorf("expected pg_dump, got %s", dumper.Tool())
	}
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
