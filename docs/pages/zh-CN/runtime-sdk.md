# Guest Runtime SDK

`@chaitin-ai/agent-compose-runtime-sdk` 运行在 agent-compose guest 内，是工作区
侧的 API；它不是 daemon 控制面 client，也不能管理 project 或 sandbox。

## 安装和运行

```bash
npm install @chaitin-ai/agent-compose-runtime-sdk
# guest image 也可以离线安装与版本匹配的 tarball：
npm install --offline /opt/agent-compose/npm/agent-compose-runtime-sdk.tgz
```

```js
import runtime from "@chaitin-ai/agent-compose-runtime-sdk";

const result = await runtime.exec("node", ["--version"], {
  streamOutput: false,
});
runtime.log("runtime ready", { success: result.success });
```

包要求使用其 `engines` 字段声明的 Node.js 版本。需要兼容性时，应使用与
daemon/runtime image 同一 release 的 SDK。

## API 总览

导出的能力包括 `exec`、`shell`、`agent`、`llm`、`workflow`、
`workflowFile`、`env`、`paths`、`log`、`report` 和 `ssh`。命令结果包含退出状态
和有上限的输出；`agent` 与 `llm` 支持使用普通 JSON Schema 或 Zod Schema 校验
结构化输出。

`ssh.prepareConfig` 会写入私有 SSH config，并在提供密钥时使用受限文件权限。
可将返回的 config 交给 `ssh.command` 或 `ssh.scpCommand`；不要记录私钥内容。
`report` helper 会把报告写在 runtime 报告边界内，不会暴露任意宿主路径。

参见[完整的包 API 文档](https://github.com/chaitin/agent-compose/blob/main/runtime/agent-compose-runtime-sdk/README.md)
和 [Guest Image ABI](guest-image-abi.html)。

## 完整示例

```js
import { runtime } from "@chaitin-ai/agent-compose-runtime-sdk";

const check = await runtime.shell('test -d "$WORKSPACE" && pwd', {
  streamOutput: false,
});
if (!check.success) {
  throw new Error(check.stderr || "workspace check failed");
}
await runtime.report.writeMarkdown("summary.md", `# Check\n\n${check.stdout}`);
```

provider 凭据应放在标记为 secret 的环境变量中。工作区文件、命令输出和 Agent
返回内容都应按不可信数据处理。
