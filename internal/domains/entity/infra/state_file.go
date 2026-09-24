package infra

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/apsdsm/joka/internal/domains/entity/domain"
)

// StateFilePath is where the state file lives.
//
// An explicit path — `--statefile`, or `statefile:` in .jokarc.yaml — wins.
// Otherwise it is the directory joka is run from, the way terraform keeps its
// state beside the configuration it applies.
//
// The profile is in the default name because one directory syncs several
// databases. Without it, running `--profile dev1` would overwrite the state
// describing `local`, and the next local sync would find its entities untracked
// and insert a second copy of every one of them.
func StateFilePath(explicit, profile string) string {
	if explicit != "" {
		return explicit
	}
	if profile == "" {
		return "joka.state.json"
	}
	return fmt.Sprintf("joka.%s.state.json", profile)
}

// StateDocument is what the file holds: the state, and the markers naming the
// database it describes.
//
// The markers are the reason the file is worth keeping. State that lives inside
// the database it describes is always self-consistent, so it can never report
// that this is the wrong database, or the right one restored from an older
// dump. A copy outside it can.
type StateDocument struct {
	// Identity is the database's state_identity marker.
	Identity string `json:"identity"`
	// Version is the state_version it was written at.
	Version int `json:"version"`

	WrittenBy string `json:"written_by,omitempty"`
	WrittenAt string `json:"written_at,omitempty"`

	State *domain.State `json:"state"`
}

// NewIdentity returns a fresh database identity.
func NewIdentity() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generating a state identity: %w", err)
	}
	return hex.EncodeToString(buf), nil
}

// WriteStateFile materializes the document at path.
//
// It writes a temporary file beside the target and renames it, so a reader
// never sees half a document and a crash part-way leaves the previous one
// intact. Renaming within a directory is atomic on POSIX.
func WriteStateFile(path string, doc StateDocument) error {
	body, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding the state file: %w", err)
	}
	body = append(body, '\n')

	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".joka-state-*")
	if err != nil {
		return fmt.Errorf("creating the state file: %w", err)
	}
	defer os.Remove(tmp.Name())

	if _, err := tmp.Write(body); err != nil {
		tmp.Close() //nolint:errcheck
		return fmt.Errorf("writing the state file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close() //nolint:errcheck
		return fmt.Errorf("flushing the state file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("closing the state file: %w", err)
	}

	if err := os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("replacing the state file: %w", err)
	}

	return nil
}

// ReadStateFile reads the document at path. found is false when there is no
// file, which is not an error: a database nobody has synced from here has none.
func ReadStateFile(path string) (StateDocument, bool, error) {
	body, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return StateDocument{}, false, nil
	}
	if err != nil {
		return StateDocument{}, false, fmt.Errorf("reading %s: %w", path, err)
	}

	var doc StateDocument
	if err := json.Unmarshal(body, &doc); err != nil {
		return StateDocument{}, false, fmt.Errorf("decoding %s: %w", path, err)
	}

	return doc, true, nil
}

// Stamp fills in who wrote the document and when.
func (d *StateDocument) Stamp(jokaVersion string) {
	d.WrittenBy = jokaVersion
	d.WrittenAt = time.Now().UTC().Format("2006-01-02 15:04:05")
}
