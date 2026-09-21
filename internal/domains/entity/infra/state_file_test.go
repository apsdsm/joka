package infra_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/apsdsm/joka/internal/domains/entity/domain"
	"github.com/apsdsm/joka/internal/domains/entity/infra"
)

func TestStateFilePath(t *testing.T) {
	if got := infra.StateFilePath(""); got != "joka.state.json" {
		t.Errorf("expected joka.state.json, got %q", got)
	}

	// The profile is in the name because one directory syncs several
	// databases. Without it, --profile dev1 would overwrite the state
	// describing local, and the next local sync would find its entities
	// untracked and insert a second copy of every one of them.
	if got := infra.StateFilePath("dev1"); got != "joka.dev1.state.json" {
		t.Errorf("expected joka.dev1.state.json, got %q", got)
	}
}

func TestStateFileRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "joka.state.json")

	state := domain.NewState()
	state.TrackFile("users.yaml", "hash-a")
	state.Track("admin", domain.EntityState{
		Table: "users", PKColumn: "id", PKValue: 7, File: "users.yaml",
		Columns: map[string]string{"email": "deadbeef"},
	})

	doc := infra.StateDocument{Identity: "db-aaaa", Version: 7, State: state}
	doc.Stamp("0.15.0-test")

	if err := infra.WriteStateFile(path, doc); err != nil {
		t.Fatalf("WriteStateFile: %v", err)
	}

	got, found, err := infra.ReadStateFile(path)
	if err != nil {
		t.Fatalf("ReadStateFile: %v", err)
	}
	if !found {
		t.Fatal("expected the file found")
	}

	if got.Identity != "db-aaaa" || got.Version != 7 {
		t.Errorf("expected the markers carried, got identity=%q version=%d", got.Identity, got.Version)
	}
	if got.WrittenBy != "0.15.0-test" || got.WrittenAt == "" {
		t.Errorf("expected the stamp carried, got %+v", got)
	}

	admin, ok := got.State.Row("admin")
	if !ok || admin.PKValue != 7 {
		t.Fatalf("expected the state carried, got %+v ok=%v", admin, ok)
	}
	if hash, recorded := admin.Baseline("email"); !recorded || hash != "deadbeef" {
		t.Errorf("expected the baseline carried, got %q recorded=%v", hash, recorded)
	}
}

func TestReadStateFileWhenThereIsNone(t *testing.T) {
	// A database nobody has synced from this directory has no file, which is a
	// fact about it rather than a failure to read it.
	_, found, err := infra.ReadStateFile(filepath.Join(t.TempDir(), "absent.json"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if found {
		t.Error("expected found false")
	}
}

func TestWriteStateFileReplacesAtomically(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "joka.state.json")

	first := domain.NewState()
	first.TrackFile("a.yaml", "hash-a")
	if err := infra.WriteStateFile(path, infra.StateDocument{Version: 1, State: first}); err != nil {
		t.Fatalf("first write: %v", err)
	}

	second := domain.NewState()
	second.TrackFile("b.yaml", "hash-b")
	if err := infra.WriteStateFile(path, infra.StateDocument{Version: 2, State: second}); err != nil {
		t.Fatalf("second write: %v", err)
	}

	got, _, err := infra.ReadStateFile(path)
	if err != nil {
		t.Fatalf("ReadStateFile: %v", err)
	}
	if _, tracked := got.State.FileHash("a.yaml"); tracked {
		t.Error("expected the second write to replace the first")
	}
	if got.Version != 2 {
		t.Errorf("expected version 2, got %d", got.Version)
	}

	// The temporary file is written beside the target and renamed, so nothing
	// is left behind for the next reader to trip over.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("reading the directory: %v", err)
	}
	if len(entries) != 1 {
		t.Errorf("expected only the state file, got %d entries", len(entries))
	}
}

func TestNewIdentityIsUnique(t *testing.T) {
	a, err := infra.NewIdentity()
	if err != nil {
		t.Fatalf("NewIdentity: %v", err)
	}
	b, err := infra.NewIdentity()
	if err != nil {
		t.Fatalf("NewIdentity: %v", err)
	}

	if a == b {
		t.Error("expected two identities to differ")
	}
	if len(a) != 32 {
		t.Errorf("expected 16 bytes of hex, got %d characters", len(a))
	}
}
