import { describe, expect, it } from "vitest";

import { parseNPMPackEntries } from "../scripts/npm-pack-result.mjs";

describe("parseNPMPackEntries", () => {
  it("accepts the npm 11 array format", () => {
    expect(parseNPMPackEntries('[{"filename":"runtime-sdk.tgz"}]')).toEqual([
      { filename: "runtime-sdk.tgz" },
    ]);
  });

  it("accepts the npm 12 package-keyed object format", () => {
    expect(
      parseNPMPackEntries(
        '{"@chaitin-ai/agent-compose-runtime-sdk":{"filename":"runtime-sdk.tgz"}}',
      ),
    ).toEqual([{ filename: "runtime-sdk.tgz" }]);
  });

  it("rejects non-collection JSON values", () => {
    expect(() => parseNPMPackEntries("null")).toThrow(
      "npm pack returned an unexpected JSON value",
    );
  });
});
