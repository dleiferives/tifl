package tasks

// The helpers here read a TaskType's content/response JSON blobs without
// committing the rest of the system to a typed schema. The database stores
// content and response as opaque JSON owned by the task type (see
// context/task-system.md "Database Schema"); only the type that produced a blob
// knows how to read it, so each type reaches for these tolerant accessors rather
// than decoding into a fixed struct that a slightly-off LLM response would break.

// asString returns m[key] as a string, or "" if absent or not a string.
func asString(m map[string]any, key string) string {
	s, _ := m[key].(string)
	return s
}

// asInt returns m[key] as an int. JSON numbers decode to float64 through
// encoding/json, so we accept both float64 and int. ok is false when the key is
// absent or not numeric.
func asInt(m map[string]any, key string) (int, bool) {
	switch v := m[key].(type) {
	case float64:
		return int(v), true
	case int:
		return v, true
	case int64:
		return int(v), true
	default:
		return 0, false
	}
}

// pick returns a new map holding only the named keys present in m. It is the
// allowlist behind TaskType.Present: presentation is default-deny, so a field
// reaches the learner only when a type explicitly lists it. Answer keys
// (correct_index, acceptable_forms) and internal item ids are dropped by
// omission, and a new content field never leaks to the client until a type opts
// it in.
func pick(m map[string]any, keys ...string) map[string]any {
	out := make(map[string]any, len(keys))
	for _, k := range keys {
		if v, ok := m[k]; ok {
			out[k] = v
		}
	}
	return out
}

// asStringSlice returns m[key] as a []string, tolerating the []any that JSON
// decoding produces as well as a native []string. Non-string elements are
// skipped; an absent or wrong-typed key yields nil.
func asStringSlice(m map[string]any, key string) []string {
	switch v := m[key].(type) {
	case []string:
		return v
	case []any:
		out := make([]string, 0, len(v))
		for _, e := range v {
			if s, ok := e.(string); ok {
				out = append(out, s)
			}
		}
		return out
	default:
		return nil
	}
}
