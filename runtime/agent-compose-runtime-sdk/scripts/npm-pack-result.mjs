export function parseNPMPackEntries(stdout) {
  const result = JSON.parse(stdout);
  if (Array.isArray(result)) {
    return result;
  }
  if (result && typeof result === "object") {
    return Object.values(result);
  }
  throw new Error("npm pack returned an unexpected JSON value");
}
