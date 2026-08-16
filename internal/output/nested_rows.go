package output

// rowsCarryNestedStructure reports whether any cell in rows holds a
// non-empty object or a non-empty array of objects.
//
// A table cell cannot hold a nested table, so the sub-table renderer
// switches to per-row blocks when this is true. Canonical case: the
// per-VM rows of `vm-usage`, whose `segments` and `shapeSeconds` cells
// collapsed to `[1 entry]` and hid the only numbers the report exists to
// deliver. Regression target: goal 19 A18.
func rowsCarryNestedStructure(rows []map[string]any) bool {
	for _, r := range rows {
		for _, v := range r {
			if obj, ok := v.(map[string]any); ok && len(obj) > 0 {
				return true
			}
			if _, ok := arrayOfObjects(v); ok {
				return true
			}
		}
	}
	return false
}
