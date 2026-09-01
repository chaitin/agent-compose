import { copyFile, mkdir, readFile, rm, writeFile } from "node:fs/promises";
import path from "node:path";
import { fileURLToPath } from "node:url";

import { marked } from "marked";

const scriptDir = path.dirname(fileURLToPath(import.meta.url));
const repositoryRoot = path.resolve(scriptDir, "../..");
const pagesDir = path.join(repositoryRoot, "docs", "pages");
const outputDir = path.join(repositoryRoot, "build", "pages");
const manuals = [
  {
    source: "connect-transport-matrix.md",
    output: "connect-transport-matrix.html",
    title: "Connect Transport Support Matrix",
    lang: "en",
    alternate: "zh-CN/connect-transport-matrix.html",
  },
  {
    source: "command-line-manual.md",
    output: "command-line-manual.html",
    title: "Command-line Manual",
    lang: "en",
    alternate: "zh-CN/command-line-manual.html",
  },
  {
    source: "zh-CN/connect-transport-matrix.md",
    output: "zh-CN/connect-transport-matrix.html",
    title: "Connect 传输支持矩阵",
    lang: "zh-CN",
    alternate: "../connect-transport-matrix.html",
  },
  {
    source: "octobus-quickstart.md",
    output: "octobus-quickstart.html",
    title: "OctoBus Integration Quick Start",
    lang: "en",
    alternate: "zh-CN/octobus-quickstart.html",
  },
  {
    source: "agent-compose-yaml-manual.md",
    output: "agent-compose-yaml-manual.html",
    title: "agent-compose.yml Manual",
    lang: "en",
    alternate: "zh-CN/agent-compose-yaml-manual.html",
  },
  {
    source: "guest-image-abi.md",
    output: "guest-image-abi.html",
    title: "Custom Guest Image ABI",
    lang: "en",
    alternate: "zh-CN/guest-image-abi.html",
  },
  {
    source: "zh-CN/command-line-manual.md",
    output: "zh-CN/command-line-manual.html",
    title: "命令行手册",
    lang: "zh-CN",
    alternate: "../command-line-manual.html",
  },
  {
    source: "zh-CN/octobus-quickstart.md",
    output: "zh-CN/octobus-quickstart.html",
    title: "OctoBus 集成快速开始",
    lang: "zh-CN",
    alternate: "../octobus-quickstart.html",
  },
  {
    source: "zh-CN/agent-compose-yaml-manual.md",
    output: "zh-CN/agent-compose-yaml-manual.html",
    title: "agent-compose.yml 配置手册",
    lang: "zh-CN",
    alternate: "../agent-compose-yaml-manual.html",
  },
  {
    source: "zh-CN/guest-image-abi.md",
    output: "zh-CN/guest-image-abi.html",
    title: "自定义 Guest Image ABI",
    lang: "zh-CN",
    alternate: "../guest-image-abi.html",
  },
];

marked.use({
  gfm: true,
  breaks: false,
});

await rm(outputDir, { recursive: true, force: true });
await mkdir(path.join(outputDir, "zh-CN"), { recursive: true });
await Promise.all([
  copyFile(path.join(pagesDir, "index.html"), path.join(outputDir, "index.html")),
  copyFile(path.join(pagesDir, "manual.css"), path.join(outputDir, "manual.css")),
]);

for (const manual of manuals) {
  const markdown = await readFile(path.join(pagesDir, manual.source), "utf8");
  const body = await marked.parse(markdown);
  const html = renderPage(manual, body);
  const destination = path.join(outputDir, manual.output);
  await writeFile(destination, html, "utf8");
}

function renderPage(manual, body) {
  const { title, output: currentPage, lang, alternate } = manual;
  const nested = currentPage.includes("/");
  const root = nested ? "../" : "./";
  const labels = lang === "zh-CN"
    ? ["首页", "命令行手册", "YAML 配置手册", "Connect 传输矩阵"]
    : ["Home", "CLI Manual", "YAML Manual", "Connect Transport"];
  const navItems = [
    [root, labels[0], ""],
    ["command-line-manual.html", labels[1], "command-line-manual.html"],
    ["agent-compose-yaml-manual.html", labels[2], "agent-compose-yaml-manual.html"],
    ["connect-transport-matrix.html", labels[3], "connect-transport-matrix.html"],
  ];
  const nav = navItems
    .map(([href, label, page]) => {
      const active = page === path.basename(currentPage) ? ' class="active" aria-current="page"' : "";
      return `<a href="${href}"${active}>${label}</a>`;
    })
    .join("\n          ");
  const alternateLabel = lang === "zh-CN" ? "English" : "中文";
  const description = lang === "zh-CN"
    ? `${title}，agent-compose 官方文档`
    : `${title}, official agent-compose documentation`;

  return `<!doctype html>
<html lang="${lang}">
  <head>
    <meta charset="utf-8">
    <meta name="viewport" content="width=device-width, initial-scale=1">
    <meta name="description" content="${escapeAttribute(description)}">
    <title>${escapeHTML(title)} · agent-compose</title>
    <link rel="stylesheet" href="${root}manual.css">
  </head>
  <body>
    <header class="manual-header">
      <div class="manual-header-inner">
        <a class="manual-brand" href="${root}">agent-<span>compose</span></a>
        <nav aria-label="Documentation">
          ${nav}
        </nav>
        <a class="language-link" href="${alternate}">${alternateLabel}</a>
        <a class="repository-link" href="https://github.com/chaitin/agent-compose">GitHub</a>
      </div>
    </header>
    <main class="manual-layout">
      <article class="markdown-body">
${body}
      </article>
    </main>
  </body>
</html>
`;
}

function escapeHTML(value) {
  return value
    .replaceAll("&", "&amp;")
    .replaceAll("<", "&lt;")
    .replaceAll(">", "&gt;")
    .replaceAll('"', "&quot;")
    .replaceAll("'", "&#39;");
}

function escapeAttribute(value) {
  return escapeHTML(value).replaceAll("`", "&#96;");
}
