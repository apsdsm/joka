package app

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/apsdsm/joka/internal/domains/entity/domain"
	"golang.org/x/crypto/argon2"
)

// SecretResolver resolves a named secret source and key (from the `secrets:`
// config map) to the secret's value. Implemented by internal/secrets.
type SecretResolver interface {
	Resolve(ctx context.Context, source, key string) (string, error)
}

// secretRefPrefix marks a template argument as a secret reference of the form
// asm.<source>.<key> rather than a literal value.
const secretRefPrefix = "asm."

// resolveColumns processes template expressions in column values. String values
// containing {{ ... }} are resolved:
//   - {{ now }} — replaced with the provided now timestamp string
//   - {{ <ref>.id }} — replaced with the auto-increment id from refMap
//   - {{ argon2id|<raw> }} — replaced with an argon2id hash of <raw>
//   - {{ sha256|<raw> }} — replaced with the SHA-256 hex digest of <raw>
//   - {{ lookup|table,return_col,where_col=value }} — replaced with a value queried from an existing table row
//   - {{ asm.<source>.<key> }} — replaced with a value from a configured secret source
//
// An argon2id/sha256 argument starting with "asm." is resolved as a secret
// reference before hashing; any other argument is a literal.
//
// Non-string values pass through unchanged.
func resolveColumns(ctx context.Context, columns map[string]any, refMap map[string]int64, now string, db DBAdapter, secrets SecretResolver) (map[string]any, error) {
	resolved := make(map[string]any, len(columns))

	for k, v := range columns {
		str, ok := v.(string)
		if !ok {
			resolved[k] = v
			continue
		}

		val, err := resolveValue(ctx, str, refMap, now, db, secrets)
		if err != nil {
			return nil, fmt.Errorf("column %q: %w", k, err)
		}

		resolved[k] = val
	}

	return resolved, nil
}

// templateExpr returns the inner expression of a {{ ... }} template and true if
// the value is a string wrapped in template delimiters. Otherwise it returns
// false (plain value or non-string).
func templateExpr(v any) (string, bool) {
	s, ok := v.(string)
	if !ok {
		return "", false
	}

	trimmed := strings.TrimSpace(s)
	if !strings.HasPrefix(trimmed, "{{") || !strings.HasSuffix(trimmed, "}}") {
		return "", false
	}

	return strings.TrimSpace(trimmed[2 : len(trimmed)-2]), true
}

// isNonDeterministicTemplate reports whether a raw column value resolves to a
// different result on every evaluation (argon2id uses a random salt; now is the
// current time). A before/after diff of these would always show a spurious
// change, so the planner labels them "regenerated" instead.
//
// Secret references (asm.*, alone or hashed) are included even though they are
// deterministic: the planner must never print resolved secret material, and
// short-circuiting here also avoids a Secrets Manager fetch at plan time.
func isNonDeterministicTemplate(v any) bool {
	expr, ok := templateExpr(v)
	if !ok {
		return false
	}
	return expr == "now" ||
		strings.HasPrefix(expr, "argon2id|") ||
		strings.HasPrefix(expr, secretRefPrefix) ||
		strings.HasPrefix(expr, "sha256|"+secretRefPrefix)
}

// refTemplate returns the referenced handle and true if the raw value is a
// {{ <ref>.id }} expression (a reference to another entity's auto-generated PK,
// not known until that row is inserted).
func refTemplate(v any) (string, bool) {
	expr, ok := templateExpr(v)
	if !ok {
		return "", false
	}
	if expr == "now" {
		return "", false
	}
	for _, fn := range []string{"argon2id|", "sha256|", "lookup|", secretRefPrefix} {
		if strings.HasPrefix(expr, fn) {
			return "", false
		}
	}
	if strings.HasSuffix(expr, ".id") {
		return strings.TrimSuffix(expr, ".id"), true
	}
	return "", false
}

// resolveValue checks whether a string is a template expression and resolves
// it. Non-template strings are returned as-is.
func resolveValue(ctx context.Context, s string, refMap map[string]int64, now string, db DBAdapter, secrets SecretResolver) (any, error) {
	trimmed := strings.TrimSpace(s)

	if !strings.HasPrefix(trimmed, "{{") || !strings.HasSuffix(trimmed, "}}") {
		return s, nil
	}

	expr := strings.TrimSpace(trimmed[2 : len(trimmed)-2])

	if expr == "now" {
		return now, nil
	}

	if strings.HasPrefix(expr, "argon2id|") {
		raw, err := resolveHashArg(ctx, expr[len("argon2id|"):], secrets)
		if err != nil {
			return nil, err
		}

		hash, err := hashArgon2id(raw)
		if err != nil {
			return nil, err
		}

		return hash, nil
	}

	if strings.HasPrefix(expr, "sha256|") {
		raw, err := resolveHashArg(ctx, expr[len("sha256|"):], secrets)
		if err != nil {
			return nil, err
		}

		h := sha256.Sum256([]byte(raw))

		return hex.EncodeToString(h[:]), nil
	}

	if strings.HasPrefix(expr, "lookup|") {
		return resolveLookup(ctx, expr[len("lookup|"):], db)
	}

	if strings.HasPrefix(expr, secretRefPrefix) {
		return resolveSecretRef(ctx, expr, secrets)
	}

	if strings.HasSuffix(expr, ".id") {
		ref := strings.TrimSuffix(expr, ".id")

		id, ok := refMap[ref]
		if !ok {
			return nil, fmt.Errorf("%w: %q not found in reference map", domain.ErrInvalidReference, ref)
		}

		return id, nil
	}

	return nil, fmt.Errorf("%w: %q", domain.ErrInvalidTemplate, expr)
}

// resolveHashArg returns an argon2id/sha256 argument as-is unless it is a
// secret reference (asm.<source>.<key>), in which case the secret value is
// resolved first.
func resolveHashArg(ctx context.Context, raw string, secrets SecretResolver) (string, error) {
	if !strings.HasPrefix(raw, secretRefPrefix) {
		return raw, nil
	}
	return resolveSecretRef(ctx, raw, secrets)
}

// parseSecretRef splits an "asm.<source>.<key>" reference into its source and
// key. Both must be non-empty and dot-free (the secret_id, which may contain
// slashes or dots, lives in config — not in the template).
func parseSecretRef(s string) (source, key string, ok bool) {
	parts := strings.Split(s, ".")
	if len(parts) != 3 || parts[0] != "asm" || parts[1] == "" || parts[2] == "" {
		return "", "", false
	}
	return parts[1], parts[2], true
}

// resolveSecretRef resolves an asm.<source>.<key> reference via the configured
// secret resolver.
func resolveSecretRef(ctx context.Context, ref string, secrets SecretResolver) (string, error) {
	source, key, ok := parseSecretRef(ref)
	if !ok {
		return "", fmt.Errorf("%w: %q (want asm.<source>.<key>)", domain.ErrInvalidTemplate, ref)
	}
	if secrets == nil {
		return "", fmt.Errorf("resolving %q: no secret sources configured (add a `secrets:` map to .jokarc.yaml)", ref)
	}
	return secrets.Resolve(ctx, source, key)
}

// resolveLookup parses a lookup expression of the form "table,return_col,where_col=value"
// and queries the database for the matching value.
func resolveLookup(ctx context.Context, params string, db DBAdapter) (any, error) {
	parts := strings.SplitN(params, ",", 3)
	if len(parts) != 3 {
		return nil, fmt.Errorf("%w: lookup requires 3 comma-separated params (table,return_col,where_col=value), got %d", domain.ErrInvalidTemplate, len(parts))
	}

	table := strings.TrimSpace(parts[0])
	returnCol := strings.TrimSpace(parts[1])
	whereExpr := strings.TrimSpace(parts[2])

	whereParts := strings.SplitN(whereExpr, "=", 2)
	if len(whereParts) != 2 {
		return nil, fmt.Errorf("%w: lookup where clause must be where_col=value, got %q", domain.ErrInvalidTemplate, whereExpr)
	}

	whereCol := strings.TrimSpace(whereParts[0])
	whereVal := strings.TrimSpace(whereParts[1])

	return db.LookupValue(ctx, table, returnCol, whereCol, whereVal)
}

// hashArgon2id produces an argon2id hash string in the standard encoded format.
// Uses the same parameters as the lgc_api default: m=65536, t=3, p=2.
func hashArgon2id(password string) (string, error) {
	salt := make([]byte, 16)

	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("generating salt: %w", err)
	}

	hash := argon2.IDKey([]byte(password), salt, 3, 64*1024, 2, 32)

	encoded := fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, 64*1024, 3, 2,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(hash),
	)

	return encoded, nil
}
