package app

import "testing"

func TestValuesEqual(t *testing.T) {
	t.Run("it treats JSON objects as equal regardless of key order and spacing", func(t *testing.T) {
		// PostgreSQL renders jsonb in its own key order with a space after the
		// colon; the YAML carries whatever the author typed. A value nobody
		// touched must not read as a change.
		pg := `{"en": "President / CEO", "ja": "代表者名"}`
		yaml := `{"ja":"代表者名","en":"President / CEO"}`

		if !valuesEqual(pg, yaml) {
			t.Error("expected the same object in a different key order to compare equal")
		}
	})

	t.Run("it still reports a genuine JSON change", func(t *testing.T) {
		before := `{"en": "President / CEO", "ja": "代表者名"}`
		after := `{"ja":"代表者名","en":"Chief Executive"}`

		if valuesEqual(before, after) {
			t.Error("expected a changed value inside the object to compare unequal")
		}
	})

	t.Run("it handles nested objects and arrays", func(t *testing.T) {
		a := `{"levels":[{"name":{"en":"Departments","ja":"部署"},"xid":"lvl_1"}]}`
		b := `{"levels": [{"xid": "lvl_1", "name": {"ja": "部署", "en": "Departments"}}]}`

		if !valuesEqual(a, b) {
			t.Error("expected nested objects to canonicalise")
		}
	})

	t.Run("it does not reorder array elements", func(t *testing.T) {
		// Key order is not meaningful in JSON; element order is.
		if valuesEqual(`[1,2]`, `[2,1]`) {
			t.Error("expected array order to be significant")
		}
	})

	t.Run("it compares non-JSON values raw", func(t *testing.T) {
		if !valuesEqual("hello", "hello") {
			t.Error("expected identical strings to be equal")
		}
		if valuesEqual("hello", "world") {
			t.Error("expected different strings to be unequal")
		}
	})

	t.Run("it does not reinterpret a value that is JSON on one side only", func(t *testing.T) {
		if valuesEqual(`{"a":1}`, `not json`) {
			t.Error("expected no match when only one side parses")
		}
	})

	t.Run("it leaves scalars alone", func(t *testing.T) {
		// A bare number or quoted string round-trips through JSON unchanged, so
		// canonicalising it would buy nothing and could equate values that
		// differ only in quoting.
		if valuesEqual(`"1"`, `1`) {
			t.Error("expected a quoted string and a number to stay unequal")
		}
	})

	t.Run("it does not match malformed JSON", func(t *testing.T) {
		if valuesEqual(`{"a":1`, `{"a": 1`) {
			t.Error("expected unparseable input to fall back to raw comparison")
		}
	})
}
