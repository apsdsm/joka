package app

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"strings"

	"github.com/apsdsm/joka/internal/domains/entity/domain"
	"golang.org/x/crypto/argon2"
)

// resolveColumns processes template expressions in column values. String values
// containing {{ ... }} are resolved:
//   - {{ now }} — replaced with the provided now timestamp string
//   - {{ <ref>.id }} — replaced with the auto-increment id from refMap
//   - {{ argon2id|<raw> }} — replaced with an argon2id hash of <raw>
//
// Non-string values pass through unchanged.
func resolveColumns(columns map[string]any, refMap map[string]int64, now string) (map[string]any, error) {
	resolved := make(map[string]any, len(columns))

	for k, v := range columns {
		str, ok := v.(string)
		if !ok {
			resolved[k] = v
			continue
		}

		val, err := resolveValue(str, refMap, now)
		if err != nil {
			return nil, fmt.Errorf("column %q: %w", k, err)
		}

		resolved[k] = val
	}

	return resolved, nil
}

// resolveValue checks whether a string is a template expression and resolves
// it. Non-template strings are returned as-is.
func resolveValue(s string, refMap map[string]int64, now string) (any, error) {
	trimmed := strings.TrimSpace(s)

	if !strings.HasPrefix(trimmed, "{{") || !strings.HasSuffix(trimmed, "}}") {
		return s, nil
	}

	expr := strings.TrimSpace(trimmed[2 : len(trimmed)-2])

	if expr == "now" {
		return now, nil
	}

	if strings.HasPrefix(expr, "argon2id|") {
		raw := expr[len("argon2id|"):]
		hash, err := hashArgon2id(raw)
		if err != nil {
			return nil, err
		}

		return hash, nil
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
