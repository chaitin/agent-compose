# Scheduler 脚本模板

这个目录放的是 agent-compose Scheduler 的可复制脚本模板。Scheduler 脚本运行在 `scheduler` runtime 里，不需要 `import` 或 `require`；宿主会在全局注入 `scheduler` 对象和兼容的 timer 函数。

## 编写建议

- 顶层代码尽量只做函数定义和触发器注册。
- 真实工作放在 `main(payload)` 或共享 helper 函数里。
- 返回值必须能被 JSON 序列化，成功运行后会写入 scheduler run 的 `result_json`。
- 每个触发器都写显式、稳定的 `triggerId`。
- 需要长期暂停触发器时，用 UI 或 API 禁用触发器。`scheduler.clearInterval(...)` 和 `scheduler.clearTimeout(...)` 只会移除当前脚本求值期间注册出来的触发器，不会持久禁用已经保存的触发器。

## 在 agent-compose.yml 中使用

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

`scheduler.script` 和声明式 `scheduler.triggers` 是二选一关系。需要简单 cron/interval/event/timeout 加 prompt 时用 `scheduler.triggers`；需要共享状态、多个 trigger 共用 workflow、调用 `scheduler.llm` / `scheduler.exec` / `scheduler.event.publish` 等能力时用 `scheduler.script`。inline scalar 和下面的 URL object 都使用同一套 scheduler runtime。

脚本也可以保存在独立文件中，并通过显式来源对象引用（`provider` 必填）：

```yaml
agents:
  reviewer:
    scheduler:
      script:
        provider: file
        path: ./02-interval-heartbeat.js
```

`provider: file` 使用 `path`，无 scheme 的相对路径以 compose 文件目录为基准，
也支持绝对路径和 `file://`；`provider: http` 和 `provider: git` 使用 `url`
（`http://` 或 `https://`）。这是 CLI authoring 能力：`agent-compose config` 和
`agent-compose up` 在本机获取一次并生成内联内容快照，daemon、v2 API、revision
和 scheduler 只看到脚本文本。它不是运行时 `import`，来源内容变化只会在下次
`up` 时生效。当前仍不支持 `import` / `require`、bundling、鉴权 header 或后台刷新。

## 触发器 ID

`triggerId` 是一个 Scheduler 内某条触发规则的稳定名字，不是浏览器 timer handle。agent-compose 会用它持久化调度状态、在 UI 展示触发器行、启停单个触发器、把手动运行路由到指定触发器，并把运行记录和事件记录关联回对应规则。

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

Sandbox lifecycle topics currently keep the compatibility prefix
`agent-compose.session.*`; their payloads use sandbox-shaped fields such as
`sandboxId`.

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

省略 `timezone` 时，cron 使用 daemon 的本地时区（`TZ` 优先，否则使用 `/etc/localtime`）；显式使用 `UTC` 可固定为 UTC 调度。

## 运行入口和载荷

手动运行时，agent-compose 会优先调用全局 `main(payload)`。如果没有 `main()` 且脚本只注册了一个触发器，则会调用这个触发器的 callback；如果有多个触发器，必须显式选择触发器或定义 `main()`。

- 手动运行的 `payload` 来自 `agent-compose scheduler invoke <scheduler> --payload '<json>'`；对应 API 字段为 `InvokeSchedulerRequest.payload_json`。
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
  sandboxPolicy: "sticky", // "sticky" | "new" | "reuse"
  timeout: "10m",
  title: "Scheduler Agent Sandbox",
  driver: "boxlite",
  guestImage: "agent-compose-guest:latest",
  workspaceId: "workspace-id",
  sandboxEnv: {
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
  sandboxId,
  cellId,
  agent,
  agentThreadId,
  stopReason,
  success,
  exitCode
}
```

`scheduler.llm(...)` 调用 daemon 侧 LLM 配置。通过 daemon 环境变量或 UI global env 设置 `LLM_API_PROTOCOL=chat_completions`（别名 `chat`、`chat_completion`）可切换到 OpenAI 兼容 Chat Completions 后端；默认为 `responses`（OpenAI Responses API）。该路径仅用于单次文本生成，不会创建 workspace agent sandbox。使用 `outputSchema` 时，`chat_completions` 通过 prompt 引导并设置 `json_object`，不等价于 Responses API strict JSON Schema。

```js
const result = scheduler.llm(prompt, {
  model: "gpt-5.4",
  outputSchema: schema,
});
// => { text, model, responseId, finishReason, json }
```

## 并行调用

`scheduler.llm` 和 `scheduler.agent` 是同步阻塞的，`for` 循环里逐个调用会串行等待。加 `.async` 可以让它们并行：调用立即返回一个 Promise，真正的请求在后台进行。

```js
async function main() {
  const topics = ["a", "b", "c"];
  const results = await Promise.all(
    topics.map((t) => scheduler.llm.async(`summarize ${t}`))
  );
  return results.map((r) => r.text);
}
```

`.async` 返回的是标准 Promise，`await`、`Promise.all`、`Promise.allSettled`、`.catch()`、`.finally()` 都可以正常使用。结果和错误的形状与同步版完全一致。

**但句柄的 settle 是按创建顺序串行的。** 引擎没有事件循环，每个句柄在创建时就把自己的结算任务排进队列，泵队列时按顺序执行，且每个任务都会阻塞 JS 线程直到对应调用完成。后果是：**先创建但没有 await 的慢调用，会挡住之后创建的句柄**。

```js
scheduler.llm.async("慢的");              // 没有 await
const r = await scheduler.llm.async("快的"); // 仍然要等「慢的」先结算
```

实测：单独 await「快的」11ms；前面挂一个 300ms 的弃用调用，就变成 301ms。

所以需要结果的调用请**全部放进同一个 `Promise.all`**（它们会真正并行），不要留下不 await 的句柄。这也和上面「未 await 的句柄」一节的建议一致。

同步版本保持不变，现有脚本无需改动。

### 取最快或最先成功的结果

**不要对 `.async` 句柄使用 `Promise.race` 或 `Promise.any`**。引擎没有事件循环，这两个内置组合子会退化成「取数组里的第一个」，而不是「取最快的那个」——它们会静默返回错误的结果。请改用：

```js
// 取最先完成的（无论成功失败）
const fastest = await scheduler.llm.race(["prompt-a", "prompt-b"]);

// 取最先成功的，全部失败时才 reject
const winner = await scheduler.llm.any([
  { prompt: "同一个问题", model: "model-a" },
  { prompt: "同一个问题", model: "model-b" },
]);
```

数组元素必须是 prompt 字符串，或带 `prompt` 字段的对象——对象本身同时用作 options，因此 `model`、`outputSchema` 的写法与 `scheduler.llm` 一致。其他类型（`null`、数字、数组等）会直接报错，不会被转成字符串当作 prompt 发出去。

胜负一旦确定，落败的调用会被取消，run 不会再为它们等待——这也是 `.any` 能真正当作 failover 用的原因：即使某个 provider 卡住，只要有一个先成功，整个 run 就不必等它。

「最先」指的是**引擎先观察到谁完成**，不是严格的完成时刻。两个条目在几乎同一瞬间完成时，谁被选中取决于 goroutine 调度，可能不是快了几微秒的那个。这和 JS 里 `Promise.race` 的语义一致——任何 Promise 实现都只保证「先被观察到」，不保证严格时序。差距明显时（例如一个 provider 卡住、另一个正常返回）选择是可靠的。

### 并行 agent 必须使用独立 sandbox

`scheduler.agent.async` 会把每次调用固定为 `sandboxPolicy: "new"`。并行 agent 无法共享 sandbox：它们会同时写同一个 workspace，而且先跑完的那个会把 sandbox 关掉。

未指定 `sandboxPolicy` 时自动使用 `"new"`；显式传入 `"sticky"` 或 `"reuse"` 会直接报错，而不是被悄悄改写。

**副作用**：如果一个触发器把它唯一的 `scheduler.agent(prompt)` 改成 `scheduler.agent.async(prompt)`，那么即使 Scheduler 配的是 `sandbox_policy: sticky`，该触发器的手动运行也不会再建立 sticky binding——因为它实际用的就是 `new`。这是并行 agent 的必然结果，不是可以绕开的。

```js
const reviews = await Promise.all(
  files.map((f) => scheduler.agent.async(`review ${f}`))
);
```

### 并发上限

并行调用有上限，防止脚本对一个长数组扇出时耗尽资源。可通过 Scheduler 的 env 覆盖：

| env | 默认值 | 说明 |
| --- | --- | --- |
| `LLM_MAX_CONCURRENCY` | 8 | 同时进行的 `scheduler.llm.async` 调用数 |
| `AGENT_MAX_CONCURRENCY` | 3 | 同时进行的 `scheduler.agent.async` 运行数 |

agent 的默认值明显更低，因为每个并行 agent 都是一个新 sandbox，而 sandbox 创建本身没有数量限制，且每次运行都要写数据库（SQLite 连接池默认 4 条，见 `SQLITE_MAX_OPEN_CONNS`）。

非法值或非正数会被忽略并回退到默认值。

此外，单次执行**同时未完成**的异步调用总数上限为 4096（含 `llm`、`agent`、`sleep`）。上面两个 env 控制的是同时**运行**的数量，但每个待处理句柄仍占一个 goroutine 及其栈，而这部分内存不受 QuickJS 的内存上限约束。超过时会抛错，提示先 await 当前这批。正常脚本不会碰到这个值。

### 未 await 的句柄

run 收尾时，**仍未完成的异步调用会被取消**，而不是被等到跑完。收尾发生在脚本返回之后，所以此刻还在飞的调用按定义就是被遗弃的。

```js
async function main() {
  scheduler.agent.async("长任务");   // 没有 await —— 会在 run 收尾时被取消
  return "done";
}
```

之所以是取消而不是等待：scheduler 的并发槽要等 run 结束才释放，如果 fire-and-forget 一个跑十分钟的 agent，run 就会保持 running 十分钟，期间 `concurrency_policy: skip` 会把后续触发全部跳过。

需要结果就 `await` 它。需要「发出去就不管」的语义，请用 `scheduler.event.publish` 触发另一个 Scheduler，而不是靠不 await 的句柄。

不认 ctx 取消的下游会被放弃而不是无限等待，代价是它的结果可能在 run 收尾之后才落到事件表里。

另外，被弃用的句柄会把已经拿到的响应体一直持有到本次 run 结束（引擎所依赖的 QuickJS 绑定不会提前注销这些对象）。这不会跨 run 泄漏，但如果一次 run 里弃用了大量大响应的调用，内存峰值会被明显抬高。

### 超时行为

Scheduler run 超时会中止整个执行，**已完成的并行结果不会被返回**。需要「尽力而为、收集部分结果」的场景，请自行缩小每批的规模。

## 等待

全局 `setTimeout` 在本运行时里是触发器注册器，因此 `await new Promise((r) => setTimeout(r, ms))` 永远不会 settle。请使用：

```js
scheduler.sleep(500);              // 同步阻塞 500ms
await scheduler.sleep.async(500);  // 返回 Promise，可参与 Promise.all
```

`scheduler.sleep.async` 不占用并发额度，因此不会挤占真正的 host 调用；它也不会在 run 收尾时被等待，所以没有 await 的 sleep 不会拖住整个 run。

时长必须是正整数毫秒，上限约 9.2 万亿毫秒（约 292 年，即 `time.Duration` 能表示的最大值）；超出范围会报错，而不是静默变成立即返回。

## 命令执行

`scheduler.exec(...)` 和 `scheduler.shell(...)` 会在 Scheduler 关联的 notebook runtime 里执行命令。

```js
const result = scheduler.exec({
  command: "python3",
  args: ["-V"],
  cwd: "/workspace",
  env: { FOO: "bar" },
  timeoutMs: 30000,
  maxOutputBytes: 4096,
  sandboxPolicy: "new",
  title: "Scheduler Command Sandbox",
  driver: "boxlite",
  guestImage: "agent-compose-guest:latest",
  workspaceId: "workspace-id",
  sandboxEnv: {
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
  sandboxId,
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

## Sandbox RPC

`scheduler.sandbox` 暴露 sandbox lifecycle unary RPC，参数和返回值使用 sandbox JSON shape。

```js
const created = scheduler.sandbox.createSandbox({ title: "Scheduler Sandbox" });
const sandboxId = created.sandbox.summary.sandboxId;

const current = scheduler.sandbox.getSandbox({ sandboxId });
const sandboxes = scheduler.sandbox.listSandboxes({});
const proxy = scheduler.sandbox.getSandboxProxy({ sandboxId });
const resumed = scheduler.sandbox.resumeSandbox({ sandboxId });
const stopped = scheduler.sandbox.stopSandbox({ sandboxId });
```

方法名同时支持 lower camel case 和 PascalCase，例如 `scheduler.sandbox.resumeSandbox(...)` 与 `scheduler.sandbox.ResumeSandbox(...)`。

Deprecated compatibility aliases `scheduler.session.*`、`sessionPolicy` 和 `sessionEnv` 仍会映射到 sandbox API，但新脚本应使用 `scheduler.sandbox.*`、`sandboxPolicy` 和 `sandboxEnv`。

## Runtime 信息

```js
scheduler.runtime.name; // "scheduler"
```

## 校验阶段限制

保存或校验 Scheduler 时，脚本会被求值以收集触发器。此时不要在顶层调用会执行副作用的 host API。

- `scheduler.agent`、`scheduler.llm`、`scheduler.exec`、`scheduler.shell`、`scheduler.event.publish`、`scheduler.sandbox.*` 在校验阶段不可用，它们的 `.async`、`.race`、`.any` 变体同样不可用。
- `scheduler.log` 在校验阶段是 no-op。
- `scheduler.sleep` 和 `scheduler.sleep.async` 在校验阶段不会真的等待（参数仍然会校验），避免顶层 sleep 拖住保存。
- `scheduler.state.*` 在校验阶段不会访问持久状态。

把这些调用放进 `main()` 或触发器 callback 里。

## 目录文件

- `01-manual-main.js`：最小手动运行 Scheduler，并返回可追踪结果。
- `02-interval-heartbeat.js`：带持久状态的 interval 任务，也支持手动重跑。
- `03-event-to-agent.js`：事件驱动 Scheduler，把事件交给 agent 处理。
- `04-cron-daily-summary.js`：定时 agent 任务，并记录最近运行状态。
- `05-router-with-multiple-triggers.js`：多个触发器共享一个 workflow 的推荐结构。
- `06-conditional-triggers.js`：按条件注册或清除 interval/timeout 触发器。
- `07-parallel-fanout.js`：用 `scheduler.llm.async` + `Promise.all` 并行扇出一批调用。
