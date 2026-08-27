package db

import (
	"errors"
	"testing"
)

func TestIsPostgresDSN(t *testing.T) {
	for _, tc := range []struct {
		dsn  string
		want bool
	}{
		{"postgres://user:pw@localhost:5432/db", true},
		{"postgresql://user:pw@localhost:5432/db", true},
		{"POSTGRES://user@localhost/db", true},
		{"  postgres://user@localhost/db", true},
		{"user:pw@tcp(localhost:3306)/db", false},
		{"mysql://user@localhost/db", false},
		{"", false},
	} {
		if got := IsPostgresDSN(tc.dsn); got != tc.want {
			t.Errorf("IsPostgresDSN(%q) = %v, want %v", tc.dsn, got, tc.want)
		}
	}
}

func TestOpenRejectsNonPostgres(t *testing.T) {
	// A MySQL DSN has to produce the sentence explaining the removal, not a
	// connection error from the PostgreSQL driver.
	_, err := Open("root:pw@tcp(localhost:3306)/db")
	if !errors.Is(err, ErrUnsupportedDriver) {
		t.Fatalf("expected ErrUnsupportedDriver, got: %v", err)
	}
	if want := "postgres://"; !contains(err.Error(), want) {
		t.Errorf("expected the error to say what a valid URL looks like, got %q", err)
	}
}

func contains(s, substr string) bool {
	for i := 0; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
