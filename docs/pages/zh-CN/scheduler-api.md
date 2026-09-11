# Scheduler JavaScript API

Scheduler 脚本运行在 daemon 的 QuickJS 环境。宿主会注入全局
`scheduler` 对象；脚本不能使用 `import`、`require` 或 Node.js 模块。

## 注册稳定的 trigger

```js
scheduler.interval("heartbeat", () => scheduler.agent("Check the workspace."), 60000);
scheduler.timeout("startup", () => scheduler.log("started"), 1000);
scheduler.cron("daily", "0 9 * * *", () => scheduler.agent("Prepare a report."), {
  timezone: "UTC",
});
scheduler.on("agent-compose.session.created", "session-created", (event) => {
  scheduler.log("new session", event);
});
```

trigger ID 是持久化身份，不是 timer handle。应显式设置并保持稳定；修改 ID 会
产生新的 trigger。脚本 scheduler 与声明式 `scheduler.triggers` 不能同时使用。

## 能力和返回值

脚本可以调用 `scheduler.agent`、`scheduler.llm`、`scheduler.exec`、
`scheduler.shell`、`scheduler.log` 和 `scheduler.event.publish`。
`scheduler.agent` 可以设置 sandbox 策略（`new`、`sticky`、`reuse`）、Agent、
超时、driver、guest image、workspace、环境变量和输出 schema。返回值包含成功
信息、输出、sandbox 身份和停止原因。

`scheduler.state` 用于保存可 JSON 序列化的状态：

```js
const count = scheduler.state.get("count") ?? 0;
scheduler.state.set("count", count + 1);
```

## 校验和执行

校验阶段只负责注册 trigger 和检查结构，不会执行 Agent、LLM 或 shell。真正执行
发生在 trigger 触发或手动调用 scheduler run 时。回调应设置边界明确的超时，并返回
可 JSON 序列化的结果，便于保存到 `result_json`。

Scheduler 级别的 `concurrency_policy` 是 `skip` 或 `parallel`。`skip` 会把重叠
运行记录为 skipped，不会排队；该策略作用于整个 scheduler，而不是单个 trigger。
`scheduler.agent.async` 并行执行时总是使用新的 sandbox。

## 调试

使用稳定 trigger ID、`scheduler.log` 和以下命令：

```bash
agent-compose scheduler inspect <scheduler>
agent-compose scheduler invoke <scheduler> --payload '{"source":"manual"}'
agent-compose scheduler runs <scheduler>
agent-compose scheduler logs <scheduler>
```

`clearInterval` 和 `clearTimeout` 只影响本次脚本求值期间注册的 trigger；若要持久
禁用 trigger，应使用 UI 或 API。
