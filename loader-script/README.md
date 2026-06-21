# Loader 脚本模板

这个目录放的是 agent-compose Loader 的可复制脚本模板。Loader 脚本运行在 `scheduler` runtime 里，不需要 `import` 或 `require`；宿主会在全局注入 `scheduler` 对象和兼容的 timer 函数。

## 编写建议

- 顶层代码尽量只做函数定义和触发器注册。
- 真实工作放在 `main(payload)` 或共享 helper 函数里。
- 返回值必须能被 JSON 序列化，成功运行后会写入 `resultJson`。
- 每个触发器都写显式、稳定的 `triggerId`。
- 需要长期暂停触发器时，用 UI 或 API 禁用触发器。`scheduler.clearInterval(...)` 和 `scheduler.clearTimeout(...)` 只会移除当前脚本求值期间注册出来的触发器，不会持久禁用已经保存的触发器。

## 在 agent-compose.yml 中内联

同样的 scheduler runtime 脚本可以直接写在 project compose 文件的 `scheduler.script` 中：

```yaml
agents:
  reviewer:
    provider: codex
    image: guest:v1
    scheduler:
      script: |
        scheduler.interval("heartbeat", function heartbeat() {
          return scheduler.agent("Review the latest workspace state.");
        }, 60000);

        function main(payload) {
          return { ok: true, payload };
        }
```

`scheduler.script` 和声明式 `scheduler.triggers` 是二选一关系。需要简单 cron/interval/event/timeout 加 prompt 时用 `scheduler.triggers`；需要共享状态、多个 trigger 共用 workflow、调用 `scheduler.llm` / `scheduler.exec` / `scheduler.event.publish` 等能力时用 `scheduler.script`。当前只支持 inline script，不支持 `script_file`、`import` / `require` 或 bundling。

## 触发器 ID

`triggerId` 是一个 Loader 内某条触发规则的稳定名字，不是浏览器 timer handle。agent-compose 会用它持久化调度状态、在 UI 展示触发器行、启停单个触发器、把手动运行路由到指定触发器，并把运行记录和事件记录关联回对应规则。

推荐写法：

```js
scheduler.interval("heartbeat", function heartbeat() {
  return runHeartbeat();
}, 60000);

scheduler.timeout("boot", function boot() {
  return runBoot();
}, 1000);

scheduler.on("agent-compose.session.created", "on-session-created", function onSessionCreated(event) {
  return handleEvent(event);
});

scheduler.cron("daily-summary", "0 9 * * *", function dailySummary() {
  return runDailySummary();
}, { timezone: "Asia/Shanghai" });
```

省略 `triggerId` 时 agent-compose 会生成 `auto-...` ID，但脚本改动后不容易保持稳定。

## 触发器 API

推荐使用 `scheduler.interval(...)` 和 `scheduler.timeout(...)`，这样能和普通 JavaScript timer 语义区分开。

```js
scheduler.interval(triggerId, callback, intervalMs);
scheduler.interval(triggerId, intervalMs, callback);
scheduler.timeout(triggerId, callback, delayMs);
scheduler.timeout(triggerId, delayMs, callback);
scheduler.clearInterval(triggerId);
scheduler.clearTimeout(triggerId);

scheduler.on(topic, triggerId, callback);
scheduler.on(topic, callback, triggerId);
scheduler.addEventListener(topic, triggerId, callback);

scheduler.cron(triggerId, expression, callback, options);
scheduler.cron(expression, callback, options);
scheduler.schedule(triggerId, expression, callback, options);
```

`scheduler.cron` 的 `options` 支持：

```js
{ id: "daily-summary", timezone: "Asia/Shanghai" }
```

也可以用 `{ tz: "Asia/Shanghai" }`。全局 `setInterval`、`setTimeout`、`clearInterval`、`clearTimeout` 以及 `scheduler.setInterval`、`scheduler.setTimeout` 仍然可用，主要用于兼容 JavaScript timer 写法。

## 运行入口和载荷

手动运行时，agent-compose 会优先调用全局 `main(payload)`。如果没有 `main()` 且脚本只注册了一个触发器，则会调用这个触发器的 callback；如果有多个触发器，必须显式选择触发器或定义 `main()`。

- 手动运行的 `payload` 来自 `RunLoaderNow.payloadJson`。
- `scheduler.on(...)` handler 收到事件 envelope：`{ topic, createdAt, payload }`。
- interval、timeout、cron handler 默认收到 `undefined`，除非你在脚本里自己转发自定义上下文。

## 日志、事件和状态

```js
scheduler.log(message, payload);

const published = scheduler.event.publish(topic, payloadObject);
// => { eventId, sequence, topic, correlationId }

const value = scheduler.state.get(key);
scheduler.state.set(key, value);
scheduler.state.set(key, undefined); // 等价于删除
scheduler.state.delete(key);
```

注意：

- `scheduler.log` 的 `message` 必须是非空字符串，`payload` 可选。
- `scheduler.event.publish` 的 `payload` 必须是普通 object，不能是数组、`null` 或 `undefined`。
- `scheduler.state` 的值会以 JSON 保存。`NaN`、`Infinity` 这类 JSON 不支持的数字会按字符串保存。

## Agent 和 LLM

```js
const reply = scheduler.agent(prompt, {
  agent: "codex",
  sessionPolicy: "sticky", // "sticky" | "new" | "reuse"
  timeout: "10m",
  title: "Loader Agent Session",
  driver: "boxlite",
  guestImage: "agent-compose-guest:latest",
  workspaceId: "workspace-id",
  sessionEnv: {
    API_TOKEN: { value: "token", secret: true },
  },
  outputSchema: schema,
});
```

`scheduler.agent(...)` 返回：

```js
{
  text,
  output,
  finalText,
  json,
  sessionId,
  cellId,
  agent,
  agentSessionId,
  stopReason,
  success,
  exitCode
}
```

`scheduler.llm(...)` 调用 daemon 侧 LLM 配置。通过 daemon 环境变量或 UI global env 设置 `LLM_API_PROTOCOL=chat_completions`（别名 `chat`、`chat_completion`）可切换到 OpenAI 兼容 Chat Completions 后端；默认为 `responses`（OpenAI Responses API）。该路径仅用于单次文本生成，不会创建 workspace agent session。

```js
const result = scheduler.llm(prompt, {
  model: "gpt-5.4",
  outputSchema: schema,
});
// => { text, model, responseId, finishReason, json }
```

## 命令执行

`scheduler.exec(...)` 和 `scheduler.shell(...)` 会在 Loader 关联的 notebook runtime 里执行命令。

```js
const result = scheduler.exec({
  command: "python3",
  args: ["-V"],
  cwd: "/workspace",
  env: { FOO: "bar" },
  timeoutMs: 30000,
  maxOutputBytes: 4096,
  sessionPolicy: "new",
  title: "Loader Command Session",
  driver: "boxlite",
  guestImage: "agent-compose-guest:latest",
  workspaceId: "workspace-id",
  sessionEnv: {
    COMMAND_TOKEN: { value: "token", secret: true },
  },
});

const shell = scheduler.shell("echo hello && pwd", {
  cwd: "/workspace",
  maxOutputBytes: 4096,
});
```

返回值：

```js
{
  stdout,
  stderr,
  output,
  exitCode,
  success,
  stdoutTruncated,
  stderrTruncated,
  outputTruncated,
  sessionId,
  cellId,
  artifacts
}
```

需要参数数组且不希望 shell 展开变量、管道或重定向时用 `scheduler.exec`；需要变量展开、管道、重定向或复合命令时用 `scheduler.shell`。

## 结构化输出

`outputSchema` 可以是 `scheduler.z` schema，也可以是普通 JSON Schema object。`schema` 是 `outputSchema` 的别名。

```js
function main(payload) {
  const RiskSummary = scheduler.z.object({
    summary: scheduler.z.string(),
    risk: scheduler.z.enum(["low", "high"]),
  });

  const result = scheduler.agent("Inspect the event and return risk as JSON.", {
    agent: "codex",
    outputSchema: RiskSummary,
  });

  return {
    raw: result.finalText,
    risk: result.json.risk,
  };
}
```

`scheduler.agent()` 会把 JSON Schema 传给 agent provider 的结构化输出路径，然后解析 `finalText`/`text`/`output` 中的 JSON 并放到 `result.json`。没有 `outputSchema` 时，`result.json` 是 `null`。

`scheduler.llm()` 使用相同机制。设置 `outputSchema` 后，模型返回的 `text` 必须是合法 JSON 字符串；如果传入的是 `scheduler.z` schema，宿主还会调用 schema 的 `parse` 做本地校验。

当前内置 `scheduler.z` 支持：

```js
scheduler.z.string();
scheduler.z.number();
scheduler.z.boolean();
scheduler.z.enum(["low", "high"]);
scheduler.z.array(itemSchema);
scheduler.z.object({ key: schema });
```

`scheduler.z.object(...)` 会生成 `additionalProperties: false`，并把所有字段都视为必填字段。

## Session RPC

`scheduler.session` 暴露 `SessionService` 的所有 unary RPC，参数和返回值使用 Connect/proto JSON shape。

```js
const created = scheduler.session.createSession({ title: "Loader Session" });
const sessionId = created.session.summary.sessionId;

const current = scheduler.session.getSession({ sessionId });
const sessions = scheduler.session.listSessions({});
const proxy = scheduler.session.getSessionProxy({ sessionId });
const resumed = scheduler.session.resumeSession({ sessionId });
const stopped = scheduler.session.stopSession({ sessionId });
```

方法名同时支持 lower camel case 和原始 proto 名，例如 `scheduler.session.resumeSession(...)` 与 `scheduler.session.ResumeSession(...)`。

## Runtime 信息

```js
scheduler.runtime.name; // "scheduler"
```

## 校验阶段限制

保存或校验 Loader 时，脚本会被求值以收集触发器。此时不要在顶层调用会执行副作用的 host API。

- `scheduler.agent`、`scheduler.llm`、`scheduler.exec`、`scheduler.shell`、`scheduler.event.publish`、`scheduler.session.*` 在校验阶段不可用。
- `scheduler.log` 在校验阶段是 no-op。
- `scheduler.state.*` 在校验阶段不会访问持久状态。

把这些调用放进 `main()` 或触发器 callback 里。

## 目录文件

- `01-manual-main.js`：最小手动运行 Loader，并返回可追踪结果。
- `02-interval-heartbeat.js`：带持久状态的 interval 任务，也支持手动重跑。
- `03-event-to-agent.js`：事件驱动 Loader，把事件交给 agent 处理。
- `04-cron-daily-summary.js`：定时 agent 任务，并记录最近运行状态。
- `05-router-with-multiple-triggers.js`：多个触发器共享一个 workflow 的推荐结构。
- `06-conditional-triggers.js`：按条件注册或清除 interval/timeout 触发器。
