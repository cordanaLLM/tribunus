package catalog

import _ "embed"

//go:embed snapshot.schema.json
var snapshotSchema []byte

// SnapshotSchema returns a copy of the embedded JSON Schema for Snapshot.
func SnapshotSchema() []byte {
	return append([]byte(nil), snapshotSchema...)
}
