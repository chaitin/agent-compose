import assert from "node:assert/strict";
import test from "node:test";

import { documentedYAMLSchemaFields } from "./schema-coverage.mjs";

test("excludes temporary v1 migration fields from documentation coverage", () => {
  const schema = `
type example struct {
  Name        string \`yaml:"name,omitempty"\`
  DisplayName string \`yaml:"display_name,omitempty"\`
  Description string \`yaml:"description,omitempty"\`
  Commit      string \`yaml:"commit,omitempty"\`
  Internal    string \`yaml:"-"\`
}
`;

  assert.deepEqual(
    [...documentedYAMLSchemaFields(schema)].sort(),
    ["commit", "name"],
  );
});
