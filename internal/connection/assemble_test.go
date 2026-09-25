package connection

import (
	"strings"
	"testing"

	"github.com/apsdsm/joka/config"
	"github.com/lib/pq"
)

// TestAssembledDSNCarriesAwkwardPasswords checks the path a secret-backed
// remote connection takes: assemble a DSN from the connection parts plus a
// password nobody chose for its typography, and have the driver read back
// exactly what went in.
//
// The script joka replaces URL-encoded the password by hand, so this is the
// step most likely to differ from it.
func TestAssembledDSNCarriesAwkwardPasswords(t *testing.T) {
	passwords := []string{
		"simple",
		"has space",
		"colon:inside",
		"at@sign",
		"slash/inside",
		"question?mark",
		"hash#mark",
		"percent%25",
		"amp&ersand",
		"plus+sign",
		`quote'and"double`,
		"brackets[]{}",
		`~!@#$%^&*()_+-=:;'",.<>/?`,
	}

	conn := &config.Connection{
		Host:     "db.example.com",
		Port:     5432,
		User:     "onc",
		Database: "onc",
		Params:   map[string]string{"sslmode": "require"},
	}

	for _, want := range passwords {
		t.Run(want, func(t *testing.T) {
			dsn := buildPostgresDSN(conn, want)

			// The driver's own parser, not net/url: what matters is what
			// lib/pq ends up sending, not what joka thinks it wrote.
			parsed, err := pq.ParseURL(dsn)
			if err != nil {
				t.Fatalf("the driver could not parse the DSN: %v", err)
			}

			got, ok := keyword(parsed, "password")
			if !ok {
				t.Fatalf("no password in the parsed DSN: %s", redact(parsed, want))
			}
			if got != want {
				t.Errorf("password did not round-trip:\n  sent %q\n  got  %q", want, got)
			}

			if user, _ := keyword(parsed, "user"); user != "onc" {
				t.Errorf("expected user onc, got %q", user)
			}
			if mode, _ := keyword(parsed, "sslmode"); mode != "require" {
				t.Errorf("expected sslmode=require to survive, got %q", mode)
			}
		})
	}
}

// keyword reads one value out of lib/pq's keyword/value connection string,
// which quotes values containing spaces with single quotes.
func keyword(dsn, key string) (string, bool) {
	for _, field := range splitKeywords(dsn) {
		k, v, ok := strings.Cut(field, "=")
		if !ok || k != key {
			continue
		}
		return strings.ReplaceAll(strings.Trim(v, "'"), `\'`, "'"), true
	}

	return "", false
}

// splitKeywords splits on spaces that are not inside single quotes.
func splitKeywords(dsn string) []string {
	var fields []string
	var current strings.Builder
	quoted := false

	for i := 0; i < len(dsn); i++ {
		switch c := dsn[i]; {
		case c == '\\' && i+1 < len(dsn):
			current.WriteByte(c)
			i++
			current.WriteByte(dsn[i])
		case c == '\'':
			quoted = !quoted
			current.WriteByte(c)
		case c == ' ' && !quoted:
			if current.Len() > 0 {
				fields = append(fields, current.String())
				current.Reset()
			}
		default:
			current.WriteByte(c)
		}
	}
	if current.Len() > 0 {
		fields = append(fields, current.String())
	}

	return fields
}

func redact(dsn, secret string) string {
	return strings.ReplaceAll(dsn, secret, "<password>")
}
