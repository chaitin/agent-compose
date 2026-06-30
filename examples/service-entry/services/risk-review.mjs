import { mkdir, writeFile } from "node:fs/promises";
import path from "node:path";

export async function run(input, runtime) {
  const scope = input.scope;
  const result = {
    scope,
    risk: scope === "production" ? "medium" : "low",
    summary: `Reviewed ${scope}`
  };

  await mkdir(runtime.artifactDir, { recursive: true });
  await writeFile(
    path.join(runtime.artifactDir, "risk-review.json"),
    `${JSON.stringify(result, null, 2)}\n`,
    "utf8"
  );

  return result;
}
