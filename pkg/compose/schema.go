package compose

import _ "embed"

//go:embed schema/agent-compose.manifest.schema.json
var manifestSchemaJSON []byte

func ManifestSchemaJSON() []byte {
	return append([]byte(nil), manifestSchemaJSON...)
}
