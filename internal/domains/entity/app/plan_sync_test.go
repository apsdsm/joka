package app

import (
	"context"
	"errors"
	"testing"

	"github.com/apsdsm/joka/internal/domains/entity/domain"
)

func TestPlanSyncAction(t *testing.T) {
	t.Run("it produces a before/after change for a modified column", func(t *testing.T) {
		db := newMockDBAdapter()
		db.track("client.yaml",
			domain.TrackedRow{TableName: "clients", RowPK: 4, PKColumn: "id", RefID: "c1", InsertionOrder: 0},
		)
		db.currentRows["clients|4"] = map[string]any{
			"name":          "Acme",
			"redirect_uris": "old-value",
		}

		modified := []*domain.EntityFile{
			{
				Path: "client.yaml",
				Entities: []domain.Entity{
					{Table: "clients", RefID: "c1", PKColumn: "id", Columns: map[string]any{
						"name":          "Acme",      // unchanged → no diff
						"redirect_uris": "new-value", // changed → diff
					}},
				},
			},
		}

		plan, err := (PlanSyncAction{DB: db, State: db.state, Declared: modified, Dirty: dirtySet(modified)}).Execute(context.Background())
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if len(plan.Updates) != 1 || len(plan.Updates[0].Rows) != 1 {
			t.Fatalf("expected 1 update row, got %+v", plan.Updates)
		}

		row := plan.Updates[0].Rows[0]
		if row.Table != "clients" || row.PKValue != 4 {
			t.Errorf("unexpected row target: %+v", row)
		}
		if len(row.Changes) != 1 {
			t.Fatalf("expected exactly 1 changed column (unchanged ones excluded), got %d: %+v", len(row.Changes), row.Changes)
		}
		c := row.Changes[0]
		if c.Column != "redirect_uris" || c.Before != "old-value" || c.After != "new-value" {
			t.Errorf("unexpected change: %+v", c)
		}
	})

	t.Run("it marks non-deterministic templates as regenerated rather than diffing them", func(t *testing.T) {
		db := newMockDBAdapter()
		db.track("user.yaml",
			domain.TrackedRow{TableName: "users", RowPK: 1, PKColumn: "id", RefID: "u1", InsertionOrder: 0},
		)
		db.currentRows["users|1"] = map[string]any{
			"password_hash": "$argon2id$old",
			"name":          "Alice",
		}

		modified := []*domain.EntityFile{
			{
				Path: "user.yaml",
				Entities: []domain.Entity{
					{Table: "users", RefID: "u1", PKColumn: "id", Columns: map[string]any{
						"password_hash": "{{ argon2id|secret }}",
						"name":          "Alice",
					}},
				},
			},
		}

		plan, err := (PlanSyncAction{DB: db, State: db.state, Declared: modified, Dirty: dirtySet(modified)}).Execute(context.Background())
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		row := plan.Updates[0].Rows[0]
		if len(row.Changes) != 1 {
			t.Fatalf("expected 1 change (regenerated hash; name unchanged), got %+v", row.Changes)
		}
		if row.Changes[0].Column != "password_hash" || !row.Changes[0].Regenerated {
			t.Errorf("expected password_hash regenerated, got %+v", row.Changes[0])
		}
	})

	t.Run("it summarizes inserts for new files with ref and generated notes", func(t *testing.T) {
		db := newMockDBAdapter()

		files := []*domain.EntityFile{
			{
				Path: "new.yaml",
				Entities: []domain.Entity{
					{
						Table: "users", RefID: "admin", PKColumn: "id", Columns: map[string]any{
							"name":          "Admin",
							"password_hash": "{{ argon2id|pw }}",
						},
						Children: []domain.Entity{
							{Table: "profiles", RefID: "p1", PKColumn: "id", Columns: map[string]any{
								"user_id": "{{ admin.id }}",
								"bio":     "hi",
							}},
						},
					},
				},
			},
		}

		plan, err := (PlanSyncAction{DB: db, State: db.state, Declared: files, Dirty: dirtySet(files)}).Execute(context.Background())
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if len(plan.Inserts) != 1 || len(plan.Inserts[0].Rows) != 2 {
			t.Fatalf("expected 2 insert rows (parent + child), got %+v", plan.Inserts)
		}

		noteFor := func(row RowInsertPlan, col string) (ColumnValue, bool) {
			for _, v := range row.Values {
				if v.Column == col {
					return v, true
				}
			}
			return ColumnValue{}, false
		}

		parent := plan.Inserts[0].Rows[0]
		if v, _ := noteFor(parent, "password_hash"); v.Note != "generated" {
			t.Errorf("expected password_hash note 'generated', got %q", v.Note)
		}
		if v, _ := noteFor(parent, "name"); v.Value != "Admin" {
			t.Errorf("expected name value 'Admin', got %q", v.Value)
		}

		child := plan.Inserts[0].Rows[1]
		if v, _ := noteFor(child, "user_id"); v.Note != "ref admin" {
			t.Errorf("expected user_id note 'ref admin', got %q", v.Note)
		}
	})

	t.Run("it shows the concrete value for lookups that resolve at plan time", func(t *testing.T) {
		db := newMockDBAdapter()
		db.lookupData["clients.id.xid=clnt_1"] = int64(7)

		files := []*domain.EntityFile{
			{
				Path: "person.yaml",
				Entities: []domain.Entity{
					{Table: "identities", RefID: "i1", PKColumn: "id", Columns: map[string]any{
						"provisioned_by_client_id": "{{ lookup|clients,id,xid=clnt_1 }}",
					}},
				},
			},
		}

		plan, err := (PlanSyncAction{DB: db, State: db.state, Declared: files, Dirty: dirtySet(files)}).Execute(context.Background())
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		v := plan.Inserts[0].Rows[0].Values[0]
		if v.Value != "7" || v.Note != "" {
			t.Errorf("expected resolved value 7 with no note, got %+v", v)
		}
	})

	t.Run("it defers lookups whose target row does not exist yet instead of failing the plan", func(t *testing.T) {
		// The plan runs before any inserts, so a new file may look up a row
		// that another new file in the same sync is about to insert (e.g. a
		// person referencing a client on a fresh database). That must not
		// abort the sync — apply resolves the lookup after inserts.
		db := newMockDBAdapter()

		files := []*domain.EntityFile{
			{
				Path: "person.yaml",
				Entities: []domain.Entity{
					{Table: "identities", RefID: "i1", PKColumn: "id", Columns: map[string]any{
						"provisioned_by_client_id": "{{ lookup|clients,id,xid=clnt_1 }}",
						"name":                     "Sysadmin",
					}},
				},
			},
		}

		plan, err := (PlanSyncAction{DB: db, State: db.state, Declared: files, Dirty: dirtySet(files)}).Execute(context.Background())
		if err != nil {
			t.Fatalf("expected plan to defer unresolved lookup, got error: %v", err)
		}

		noteFor := func(row RowInsertPlan, col string) (ColumnValue, bool) {
			for _, v := range row.Values {
				if v.Column == col {
					return v, true
				}
			}
			return ColumnValue{}, false
		}

		row := plan.Inserts[0].Rows[0]
		if v, _ := noteFor(row, "provisioned_by_client_id"); v.Note != "lookup, resolved at apply time" {
			t.Errorf("expected deferred lookup note, got %+v", v)
		}
		if v, _ := noteFor(row, "name"); v.Value != "Sysadmin" {
			t.Errorf("expected plain value untouched, got %+v", v)
		}
	})

	t.Run("it defers unresolved lookups in modified files as a change applied at apply time", func(t *testing.T) {
		// Same scenario as inserts, but for a tracked file: its lookup may
		// target a row inserted by a new file in this same sync (inserts
		// apply before updates).
		db := newMockDBAdapter()
		db.track("person.yaml",
			domain.TrackedRow{TableName: "identities", RowPK: 2, PKColumn: "id", RefID: "i1", InsertionOrder: 0},
		)
		db.currentRows["identities|2"] = map[string]any{
			"provisioned_by_client_id": "5",
		}

		modified := []*domain.EntityFile{
			{
				Path: "person.yaml",
				Entities: []domain.Entity{
					{Table: "identities", RefID: "i1", PKColumn: "id", Columns: map[string]any{
						"provisioned_by_client_id": "{{ lookup|clients,id,xid=clnt_1 }}",
					}},
				},
			},
		}

		plan, err := (PlanSyncAction{DB: db, State: db.state, Declared: modified, Dirty: dirtySet(modified)}).Execute(context.Background())
		if err != nil {
			t.Fatalf("expected plan to defer unresolved lookup, got error: %v", err)
		}

		row := plan.Updates[0].Rows[0]
		if len(row.Changes) != 1 {
			t.Fatalf("expected 1 deferred change, got %+v", row.Changes)
		}
		c := row.Changes[0]
		if c.Column != "provisioned_by_client_id" || !c.Deferred || c.Before != "5" {
			t.Errorf("expected deferred change with before value, got %+v", c)
		}
	})

	t.Run("it redacts secret references and never fetches them at plan time", func(t *testing.T) {
		db := newMockDBAdapter()
		db.track("client.yaml",
			domain.TrackedRow{TableName: "clients", RowPK: 4, PKColumn: "id", RefID: "c1", InsertionOrder: 0},
		)
		db.currentRows["clients|4"] = map[string]any{
			"client_secret": "$argon2id$old",
			"api_key":       "old-plain",
		}

		files := []*domain.EntityFile{
			{
				Path: "new.yaml",
				Entities: []domain.Entity{
					{Table: "keys", RefID: "k1", PKColumn: "id", Columns: map[string]any{
						"key_hash": "{{ sha256|asm.seed.admin_key }}",
						"plain":    "{{ asm.seed.plain_value }}",
					}},
				},
			},
		}
		modified := []*domain.EntityFile{
			{
				Path: "client.yaml",
				Entities: []domain.Entity{
					{Table: "clients", RefID: "c1", PKColumn: "id", Columns: map[string]any{
						"client_secret": "{{ argon2id|asm.seed.client_secret }}",
						"api_key":       "{{ asm.seed.api_key }}",
					}},
				},
			},
		}

		// PlanSyncAction takes no resolver at all: secret refs must short-circuit
		// as non-deterministic before any resolution is attempted.
		plan, err := (PlanSyncAction{DB: db, State: db.state, Declared: append(append([]*domain.EntityFile{}, files...), modified...), Dirty: dirtySet(append(append([]*domain.EntityFile{}, files...), modified...))}).Execute(context.Background())
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		for _, v := range plan.Inserts[0].Rows[0].Values {
			if v.Note != "generated" {
				t.Errorf("expected insert column %s note 'generated', got %+v", v.Column, v)
			}
			if v.Value != "" {
				t.Errorf("insert column %s materialized a value: %+v", v.Column, v)
			}
		}

		row := plan.Updates[0].Rows[0]
		if len(row.Changes) != 2 {
			t.Fatalf("expected 2 regenerated changes, got %+v", row.Changes)
		}
		for _, c := range row.Changes {
			if !c.Regenerated || c.Before != "" || c.After != "" {
				t.Errorf("expected column %s regenerated with no before/after, got %+v", c.Column, c)
			}
		}
	})

	t.Run("it still fails the plan for invalid templates", func(t *testing.T) {
		db := newMockDBAdapter()

		files := []*domain.EntityFile{
			{
				Path: "bad.yaml",
				Entities: []domain.Entity{
					{Table: "users", RefID: "u1", PKColumn: "id", Columns: map[string]any{
						"name": "{{ bogus|nope }}",
					}},
				},
			},
		}

		_, err := (PlanSyncAction{DB: db, State: db.state, Declared: files, Dirty: dirtySet(files)}).Execute(context.Background())
		if !errors.Is(err, domain.ErrInvalidTemplate) {
			t.Fatalf("expected ErrInvalidTemplate, got %v", err)
		}
	})

}

// dirtySet marks every given file as needing a write, which is what the plan
// fixtures mean by handing one over.
func dirtySet(files []*domain.EntityFile) map[string]bool {
	dirty := make(map[string]bool, len(files))
	for _, f := range files {
		dirty[f.Path] = true
	}
	return dirty
}

func TestPlanSyncReportsUndeclared(t *testing.T) {
	// An entity dropped from the file is planned for deletion when its row is
	// still there, and for a tracking drop when it is not.
	db := newMockDBAdapter()
	db.track("client.yaml",
		domain.TrackedRow{TableName: "clients", RowPK: 4, PKColumn: "id", RefID: "c1", InsertionOrder: 0},
		domain.TrackedRow{TableName: "grants", RowPK: 9, PKColumn: "id", RefID: "g1", InsertionOrder: 1},
	)
	db.currentRows["clients|4"] = map[string]any{"name": "only one"}

	files := []*domain.EntityFile{{
		Path: "client.yaml",
		Entities: []domain.Entity{
			{Table: "clients", RefID: "c1", PKColumn: "id", Columns: map[string]any{"name": "only one"}},
		},
	}}

	plan, err := (PlanSyncAction{DB: db, State: db.state, Declared: files, Dirty: dirtySet(files)}).Execute(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// g1's row was never put in currentRows, so it reads as already gone: the
	// tracking is dropped and nothing is deleted.
	if len(plan.Forgets) != 1 || plan.Forgets[0].RefID != "g1" {
		t.Fatalf("expected g1 planned as a tracking drop, got %+v", plan.Forgets)
	}
	if len(plan.Deletes) != 0 {
		t.Errorf("expected nothing deleted for a row that is already gone, got %+v", plan.Deletes)
	}

	// With the row live it becomes a delete instead.
	db.currentRows["grants|9"] = map[string]any{"x": 1}

	plan, err = (PlanSyncAction{DB: db, State: db.state, Declared: files, Dirty: dirtySet(files)}).Execute(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(plan.Deletes) != 1 || plan.Deletes[0].RefID != "g1" {
		t.Errorf("expected g1 planned for deletion, got %+v", plan.Deletes)
	}
}
