package infra

import "fmt"

// pkValueFromColumns returns the value of the named PK column from the
// resolved columns map, coerced to int64. It is used by InsertRow when the
// entity declared a non-auto-increment PK (e.g. a natural key on a join
// table). Returns ok=false when the column is not present.
func pkValueFromColumns(columns map[string]any, pkColumn string) (int64, bool, error) {
	v, ok := columns[pkColumn]
	if !ok {
		return 0, false, nil
	}

	switch x := v.(type) {
	case int64:
		return x, true, nil
	case int:
		return int64(x), true, nil
	case int32:
		return int64(x), true, nil
	case uint:
		return int64(x), true, nil
	case uint32:
		return int64(x), true, nil
	case uint64:
		return int64(x), true, nil
	case float64:
		return int64(x), true, nil
	case float32:
		return int64(x), true, nil
	}

	return 0, true, fmt.Errorf("_pk column %q has unsupported type %T", pkColumn, v)
}
