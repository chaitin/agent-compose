#!/usr/bin/env node
import { createHash } from "node:crypto";
import { isIP } from "node:net";
import { defineService, runServiceMain, grpcInvalidArgumentError, grpcNotFoundError } from "@chaitin-ai/octobus-sdk";

// No real IP telemetry or credentials. Each process has stable opaque IDs.
const rows = [
  { ip: "192.0.2.44", title: "异常登录尝试", action: "blocked", conclusion: "访问被拦截，现有记录不证明入侵成功" },
  { ip: "192.0.2.44", title: "目录探测", action: "observed", conclusion: "观察到探测请求，未发现成功执行证据" },
  { ip: "198.51.100.7", title: "正常健康检查", action: "allowed", conclusion: "正常测试流量" },
].map((row, index) => ({
  ...row,
  id: createHash("sha256").update(`ac-evidence-${index}`).digest("hex").slice(0, 20),
  evidenceCode: createHash("sha256").update(`detail-only-${index}`).digest("hex").slice(0, 12),
}));

runServiceMain(defineService({ handlers: {
  "ac.evidence.v1.EvidenceService/SearchEvents": ({ request }) => {
    if (isIP(request.ip ?? "") !== 4) throw grpcInvalidArgumentError("an IPv4 address is required");
    const selected = rows.filter((row) => row.ip === request.ip);
    return { records: selected.map(({ id, ip, title }) => ({ id, ip, title })), total: selected.length };
  },
  "ac.evidence.v1.EvidenceService/GetEvent": ({ request }) => {
    const row = rows.find((item) => item.id === request.id);
    if (!row) throw grpcNotFoundError("event not found");
    return row;
  },
} }));
