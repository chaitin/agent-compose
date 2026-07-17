// These fields preserve v1 presentation metadata during migration. They are
// intentionally absent from the v2 authoring manual and will leave with the
// compatibility path.
const v1MigrationCompatibilityFields = new Set([
  "description",
  "display_name",
]);

export function documentedYAMLSchemaFields(schema) {
  return new Set(
    [...schema.matchAll(/yaml:"([^",]+)/g)]
      .map((match) => match[1])
      .filter((field) => field !== "-" && !v1MigrationCompatibilityFields.has(field)),
  );
}
