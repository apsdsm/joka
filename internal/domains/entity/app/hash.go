package app

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
)

// HashFileContent returns the SHA-256 hex digest of the file at path.
func HashFileContent(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("reading file for hash: %w", err)
	}

	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}
