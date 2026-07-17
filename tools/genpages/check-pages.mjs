import { readFile, readdir, stat } from "node:fs/promises";
import path from "node:path";
import { fileURLToPath } from "node:url";

import { documentedYAMLSchemaFields } from "./schema-coverage.mjs";

const scriptDir = path.dirname(fileURLToPath(import.meta.url));
const repositoryRoot = path.resolve(scriptDir, "../..");
const pagesDir = path.join(repositoryRoot, "docs", "pages");
const outputDir = path.join(repositoryRoot, "build", "pages");
const schemaPath = path.join(repositoryRoot, "pkg", "compose", "spec.go");
const manualNames = [
  "agent-compose-yaml-manual.md",
  "command-line-manual.md",
  "guest-image-abi.md",
];
const expectedOutput = new Set([
  "agent-compose-yaml-manual.html",
  "command-line-manual.html",
  "guest-image-abi.html",
  "index.html",
  "manual.css",
  "zh-CN/agent-compose-yaml-manual.html",
  "zh-CN/command-line-manual.html",
  "zh-CN/guest-image-abi.html",
]);

await checkSourceLayout();
await checkEnglishManuals();
const schemaFieldCount = await checkYAMLSchemaCoverage();
await checkOutputWhitelist();
await checkInternalLinks();
await checkNavigation();
await checkABILanguageSwitch();

console.log(`Pages checks passed for ${schemaFieldCount} YAML schema fields and ${expectedOutput.size} public files`);

async function checkSourceLayout() {
  const rootMarkdown = (await readdir(pagesDir))
    .filter((name) => name.endsWith(".md"))
    .sort();
  const chineseMarkdown = (await readdir(path.join(pagesDir, "zh-CN")))
    .filter((name) => name.endsWith(".md"))
    .sort();

  assertSameEntries(rootMarkdown, manualNames, "English Pages manuals");
  assertSameEntries(chineseMarkdown, manualNames, "Chinese Pages manuals");

  const unexpectedREADME = (await listFiles(pagesDir))
    .find((name) => path.basename(name).toLowerCase() === "readme.md");
  if (unexpectedREADME) {
    throw new Error(`docs/pages must not contain README.md: ${unexpectedREADME}`);
  }
}

async function checkEnglishManuals() {
  for (const name of manualNames) {
    const source = await readFile(path.join(pagesDir, name), "utf8");
    if (/[\u3400-\u9fff]/u.test(source)) {
      throw new Error(`${name} must be written in English; move Chinese content under docs/pages/zh-CN`);
    }
  }
}

async function checkYAMLSchemaCoverage() {
  const schema = await readFile(schemaPath, "utf8");
  const fields = documentedYAMLSchemaFields(schema);

  for (const relativePath of [
    "agent-compose-yaml-manual.md",
    "zh-CN/agent-compose-yaml-manual.md",
  ]) {
    const source = await readFile(path.join(pagesDir, relativePath), "utf8");
    const missing = [...fields]
      .filter((field) => !source.includes(`\`${field}\``))
      .sort();
    if (missing.length > 0) {
      throw new Error(`${relativePath} does not reference schema fields: ${missing.join(", ")}`);
    }
  }

  return fields.size;
}

async function checkOutputWhitelist() {
  const actualOutput = new Set(await listFiles(outputDir));
  const unexpected = [...actualOutput].filter((name) => !expectedOutput.has(name));
  const missing = [...expectedOutput].filter((name) => !actualOutput.has(name));
  if (unexpected.length > 0 || missing.length > 0) {
    throw new Error([
      unexpected.length > 0 ? `unexpected Pages output: ${unexpected.join(", ")}` : "",
      missing.length > 0 ? `missing Pages output: ${missing.join(", ")}` : "",
    ].filter(Boolean).join("; "));
  }
}

async function checkInternalLinks() {
  const htmlFiles = [...expectedOutput].filter((name) => name.endsWith(".html"));
  const documents = new Map();
  for (const relativePath of htmlFiles) {
    documents.set(relativePath, await readFile(path.join(outputDir, relativePath), "utf8"));
  }

  for (const [relativePath, html] of documents) {
    for (const match of html.matchAll(/(?:href|data-href-(?:en|zh))="([^"]+)"/g)) {
      const href = match[1];
      if (isExternalLink(href)) {
        continue;
      }
      await checkLocalLink(relativePath, href, documents);
    }
  }
}

async function checkLocalLink(sourcePath, href, documents) {
  const [rawTarget, rawFragment = ""] = href.split("#", 2);
  const targetWithoutQuery = rawTarget.split("?", 1)[0];
  let targetPath = path.resolve(outputDir, path.dirname(sourcePath), decodeURIComponent(targetWithoutQuery));
  if (!isWithin(outputDir, targetPath)) {
    throw new Error(`${sourcePath} contains a link outside the Pages output: ${href}`);
  }

  const targetStats = await stat(targetPath).catch(() => undefined);
  if (targetStats?.isDirectory()) {
    targetPath = path.join(targetPath, "index.html");
  }
  const finalStats = await stat(targetPath).catch(() => undefined);
  if (!finalStats?.isFile()) {
    throw new Error(`${sourcePath} contains a broken internal link: ${href}`);
  }

  if (!rawFragment || path.extname(targetPath) !== ".html") {
    return;
  }
  const targetRelativePath = path.relative(outputDir, targetPath).split(path.sep).join("/");
  const targetHTML = documents.get(targetRelativePath) ?? await readFile(targetPath, "utf8");
  const fragment = decodeURIComponent(rawFragment);
  const escapedFragment = escapeRegExp(fragment);
  if (!new RegExp(`(?:id|name)=["']${escapedFragment}["']`).test(targetHTML)) {
    throw new Error(`${sourcePath} contains a link to a missing anchor: ${href}`);
  }
}

async function checkNavigation() {
  const htmlFiles = [...expectedOutput].filter((name) => name.endsWith(".html"));
  for (const relativePath of htmlFiles) {
    const html = await readFile(path.join(outputDir, relativePath), "utf8");
    for (const nav of html.matchAll(/<nav\b[\s\S]*?<\/nav>/g)) {
      if (nav[0].includes("guest-image-abi")) {
        throw new Error(`${relativePath} exposes Guest Image ABI in public navigation`);
      }
    }
  }

  const homepage = await readFile(path.join(outputDir, "index.html"), "utf8");
  if (homepage.includes("guest-image-abi")) {
    throw new Error("index.html must not link to Guest Image ABI");
  }
}

async function checkABILanguageSwitch() {
  const pairs = [
    ["guest-image-abi.html", "zh-CN/guest-image-abi.html"],
    ["zh-CN/guest-image-abi.html", "../guest-image-abi.html"],
  ];
  for (const [source, alternate] of pairs) {
    const html = await readFile(path.join(outputDir, source), "utf8");
    if (!html.includes(`class="language-link" href="${alternate}"`)) {
      throw new Error(`${source} does not link to its language alternate`);
    }
  }
}

async function listFiles(directory, relativeDirectory = "") {
  const entries = await readdir(path.join(directory, relativeDirectory), { withFileTypes: true });
  const files = [];
  for (const entry of entries) {
    const relativePath = path.join(relativeDirectory, entry.name);
    if (entry.isDirectory()) {
      files.push(...await listFiles(directory, relativePath));
    } else {
      files.push(relativePath.split(path.sep).join("/"));
    }
  }
  return files.sort();
}

function assertSameEntries(actual, expected, label) {
  const sortedExpected = [...expected].sort();
  if (actual.join("\n") !== sortedExpected.join("\n")) {
    throw new Error(`${label} must be: ${sortedExpected.join(", ")}; got: ${actual.join(", ")}`);
  }
}

function isExternalLink(href) {
  return href.startsWith("#")
    ? false
    : /^(?:[a-z][a-z\d+.-]*:|\/\/)/i.test(href);
}

function isWithin(parent, child) {
  const relativePath = path.relative(parent, child);
  return relativePath === "" || (!relativePath.startsWith("..") && !path.isAbsolute(relativePath));
}

function escapeRegExp(value) {
  return value.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
}
