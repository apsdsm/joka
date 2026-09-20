package app

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/apsdsm/joka/internal/domains/entity/domain"
)

func TestEntityStatusAction(t *testing.T) {
	t.Run("it reports synced when hash matches", func(t *testing.T) {
		dir := t.TempDir()
		content := []byte("entities:\n  - _is: users\n    name: A\n")
		os.WriteFile(filepath.Join(dir, "a.yaml"), content, 0644)

		hash, _ := HashFileContent(filepath.Join(dir, "a.yaml"))

		state := domain.NewState()
		state.TrackFile("a.yaml", hash)

		result, err := (EntityStatusAction{State: state, EntitiesDir: dir, Files: []string{"a.yaml"}}).Execute()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if len(result) != 1 {
			t.Fatalf("expected 1 result, got %d", len(result))
		}

		if result[0].Status != domain.StatusSynced {
			t.Errorf("expected StatusSynced, got %s", result[0].Status)
		}
	})

	t.Run("it reports modified when hash differs", func(t *testing.T) {
		dir := t.TempDir()
		os.WriteFile(filepath.Join(dir, "a.yaml"), []byte("modified content"), 0644)

		state := domain.NewState()
		state.TrackFile("a.yaml", "old_hash_that_wont_match")

		result, err := (EntityStatusAction{State: state, EntitiesDir: dir, Files: []string{"a.yaml"}}).Execute()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if len(result) != 1 {
			t.Fatalf("expected 1 result, got %d", len(result))
		}

		if result[0].Status != domain.StatusModified {
			t.Errorf("expected StatusModified, got %s", result[0].Status)
		}
	})

	t.Run("it reports new for unsynced files", func(t *testing.T) {
		dir := t.TempDir()
		os.WriteFile(filepath.Join(dir, "new.yaml"), []byte("new content"), 0644)

		state := domain.NewState()

		result, err := (EntityStatusAction{State: state, EntitiesDir: dir, Files: []string{"new.yaml"}}).Execute()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if len(result) != 1 {
			t.Fatalf("expected 1 result, got %d", len(result))
		}

		if result[0].Status != domain.StatusNew {
			t.Errorf("expected StatusNew, got %s", result[0].Status)
		}
	})

	t.Run("it reports orphaned for tracked files missing from disk", func(t *testing.T) {
		dir := t.TempDir()

		state := domain.NewState()
		state.TrackFile("deleted.yaml", "somehash")

		result, err := (EntityStatusAction{State: state, EntitiesDir: dir, Files: []string{}}).Execute()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if len(result) != 1 {
			t.Fatalf("expected 1 result, got %d", len(result))
		}

		if result[0].Status != domain.StatusOrphaned {
			t.Errorf("expected StatusOrphaned, got %s", result[0].Status)
		}
	})

	t.Run("it reports modified when stored hash is empty", func(t *testing.T) {
		dir := t.TempDir()
		os.WriteFile(filepath.Join(dir, "legacy.yaml"), []byte("content"), 0644)

		state := domain.NewState()
		state.TrackFile("legacy.yaml", "")

		result, err := (EntityStatusAction{State: state, EntitiesDir: dir, Files: []string{"legacy.yaml"}}).Execute()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if len(result) != 1 {
			t.Fatalf("expected 1 result, got %d", len(result))
		}

		if result[0].Status != domain.StatusModified {
			t.Errorf("expected StatusModified for empty hash, got %s", result[0].Status)
		}
	})

	t.Run("it handles mixed statuses across multiple files", func(t *testing.T) {
		dir := t.TempDir()

		syncedContent := []byte("synced")
		os.WriteFile(filepath.Join(dir, "synced.yaml"), syncedContent, 0644)
		syncedHash, _ := HashFileContent(filepath.Join(dir, "synced.yaml"))

		os.WriteFile(filepath.Join(dir, "modified.yaml"), []byte("changed"), 0644)
		os.WriteFile(filepath.Join(dir, "new.yaml"), []byte("brand new"), 0644)

		state := domain.NewState()
		state.TrackFile("synced.yaml", syncedHash)
		state.TrackFile("modified.yaml", "stale_hash")
		state.TrackFile("orphaned.yaml", "dead_hash")

		result, err := (EntityStatusAction{
			State:       state,
			EntitiesDir: dir,
			Files:       []string{"synced.yaml", "modified.yaml", "new.yaml"},
		}).Execute()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if len(result) != 4 {
			t.Fatalf("expected 4 results, got %d", len(result))
		}

		statuses := make(map[string]domain.FileStatus)
		for _, r := range result {
			statuses[r.Path] = r.Status
		}

		if statuses["synced.yaml"] != domain.StatusSynced {
			t.Errorf("expected synced.yaml=synced, got %s", statuses["synced.yaml"])
		}
		if statuses["modified.yaml"] != domain.StatusModified {
			t.Errorf("expected modified.yaml=modified, got %s", statuses["modified.yaml"])
		}
		if statuses["new.yaml"] != domain.StatusNew {
			t.Errorf("expected new.yaml=new, got %s", statuses["new.yaml"])
		}
		if statuses["orphaned.yaml"] != domain.StatusOrphaned {
			t.Errorf("expected orphaned.yaml=orphaned, got %s", statuses["orphaned.yaml"])
		}
	})
}
