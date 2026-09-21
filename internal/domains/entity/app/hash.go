package app

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
)

// HashValue returns the SHA-256 hex digest of one applied column value.
//
// The baseline stores hashes rather than values for three reasons, in order of
// weight. Secrets: resolveColumns turns {{ asm.… }} into the actual secret
// before inserting, and the planner goes out of its way never to materialize
// one — storing values would undo that and put Secrets Manager plaintext into
// any state anyone copies. Size: a hash is 64 bytes whatever the column holds,
// so a large JSON column does not bloat the document. And nothing needs the
// value back: a three-way merge asks whether a column changed, and both sides
// it would show are read live.
//
// The value is rendered with normalizeValue and then canonicalised the way
// valuesEqual canonicalises, so a hash and a diff cannot disagree about what a
// value is. That matters most for jsonb: PostgreSQL renders it in its own key
// order with a space after each colon, so the declaration and the live row are
// textually different for a value nobody has touched. Hashing the raw text
// would make every JSON column look like drift on every run.
func HashValue(v any) string {
	s := normalizeValue(v)
	if canonical, ok := canonicalJSON(s); ok {
		s = canonical
	}

	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// BaselineOf hashes every column of a resolved row.
func BaselineOf(columns map[string]any) map[string]string {
	if len(columns) == 0 {
		return nil
	}

	out := make(map[string]string, len(columns))
	for name, value := range columns {
		out[name] = HashValue(value)
	}
	return out
}

// HashFileContent returns the SHA-256 hex digest of the file at path.
func HashFileContent(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("reading file for hash: %w", err)
	}

	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}
