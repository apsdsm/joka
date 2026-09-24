package app_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/apsdsm/joka/internal/domains/entity/app"
	"github.com/apsdsm/joka/internal/domains/entity/domain"
)

func deletedRows() []domain.TrackedRow {
	return []domain.TrackedRow{
		{RefID: "m3", TableName: "mappings", PKColumn: "id", RowPK: 3, EntityFile: "m.yaml"},
		{RefID: "m2", TableName: "mappings", PKColumn: "id", RowPK: 2, EntityFile: "m.yaml"},
	}
}

func TestDeleteRefusedError(t *testing.T) {
	t.Run("it names every row rather than counting them", func(t *testing.T) {
		// The count was the only thing jjc2's CI printed, and 16 rows went with
		// it. JSON has no plan in front of it, so the error carries the list.
		err := app.DeleteRefusedError(deletedRows())
		if !errors.Is(err, domain.ErrDeleteNotAllowed) {
			t.Fatalf("expected ErrDeleteNotAllowed, got: %v", err)
		}
		for _, want := range []string{"m2", "m3", "mappings", "m.yaml", "--allow-delete"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("expected the refusal to mention %q, got: %v", want, err)
			}
		}
	})

	t.Run("it orders the rows so two runs read the same", func(t *testing.T) {
		err := app.DeleteRefusedError(deletedRows())
		if strings.Index(err.Error(), "m2") > strings.Index(err.Error(), "m3") {
			t.Errorf("expected the rows sorted by _id, got: %v", err)
		}
	})

	t.Run("no deletes is no error", func(t *testing.T) {
		if err := app.DeleteRefusedError(nil); err != nil {
			t.Errorf("expected nil for an empty list, got: %v", err)
		}
		if err := app.DeleteRefusedSummary(nil); err != nil {
			t.Errorf("expected nil for an empty list, got: %v", err)
		}
	})
}

func TestDeleteRefusedSummary(t *testing.T) {
	t.Run("it counts and points at the plan instead of repeating it", func(t *testing.T) {
		// The plan has already named every row, first and in a layout an error
		// string cannot match. Printing them again under Error: says it twice.
		err := app.DeleteRefusedSummary(deletedRows())
		if !errors.Is(err, domain.ErrDeleteNotAllowed) {
			t.Fatalf("expected ErrDeleteNotAllowed, got: %v", err)
		}
		if !strings.Contains(err.Error(), "2 rows") {
			t.Errorf("expected a count, got: %v", err)
		}
		if strings.Contains(err.Error(), "m.yaml") {
			t.Errorf("expected the summary not to repeat the row list, got: %v", err)
		}
		if !strings.Contains(err.Error(), "--allow-delete") {
			t.Errorf("expected the summary to name the flag, got: %v", err)
		}
	})
}
