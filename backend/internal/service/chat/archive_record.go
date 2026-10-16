package chat

import (
	"encoding/json"
	"reflect"
)

// dropEmptyArchiveFields removes the entries of a provider-event archive record
// that hold nothing: nil, a zero string or pointer, an empty slice, a JSON null.
// The record has 26 keys and a streamed delta sets about five of them, so the rest
// used to be stored as null or "" on every row.
//
// An absent key reads exactly like an empty one. The only SQL that reads the
// payload, SelectVerifiedAuthenticationTurn, uses json_extract, which returns NULL
// for both, and its one comparison with the empty string goes through COALESCE. A Go
// decoder gives both the zero value.
//
// Call it before adding a key whose presence means something. "error" is one: that
// query tests "$.error IS NULL", so an error with an empty message has to stay in
// the row for the turn to remain a failure.
func dropEmptyArchiveFields(record map[string]any) {
	for key, value := range record {
		if emptyArchiveField(value) {
			delete(record, key)
		}
	}
}

func emptyArchiveField(value any) bool {
	if raw, ok := value.(json.RawMessage); ok {
		return len(raw) == 0 || string(raw) == "null"
	}
	v := reflect.ValueOf(value)
	return !v.IsValid() || v.IsZero() || (v.Kind() == reflect.Slice && v.Len() == 0)
}
