import { parse } from "acorn";
import type { Node } from "acorn";
import { sha256 } from "./hash.js";
import type { ParsedWorkflowScript, WorkflowMeta } from "./types.js";

type AstNode = Node & Record<string, unknown>;

const reservedKeys = new Set(["__proto__", "constructor", "prototype"]);

export function parseWorkflowScript(source: string): ParsedWorkflowScript {
  const program = parse(source, {
    ecmaVersion: "latest",
    sourceType: "module",
    allowAwaitOutsideFunction: true,
    allowReturnOutsideFunction: true,
  }) as unknown as AstNode;
  const statements = program.body as AstNode[];
  const first = statements[0];
  if (!first || first.type !== "ExportNamedDeclaration") {
    throw new Error("workflow first statement must be export const meta = {...}");
  }
  const declaration = first.declaration as AstNode | null;
  if (!declaration || declaration.type !== "VariableDeclaration" || declaration.kind !== "const") {
    throw new Error("workflow first statement must be export const meta = {...}");
  }
  const declarations = declaration.declarations as AstNode[];
  if (declarations.length !== 1) {
    throw new Error("workflow meta export must declare only meta");
  }
  const declarator = declarations[0];
  const identifier = declarator.id as AstNode;
  if (identifier.type !== "Identifier" || identifier.name !== "meta") {
    throw new Error("workflow first statement must export const meta");
  }

  const metaValue = evaluateLiteral(declarator.init as AstNode, "meta");
  const meta = validateMeta(metaValue);
  validateDeterministicScript(program);

  const start = first.start;
  const end = first.end;
  return {
    meta,
    body: `${source.slice(0, start)}${source.slice(end)}`,
    scriptHash: sha256(source),
  };
}

function validateMeta(value: unknown): WorkflowMeta {
  if (!isPlainRecord(value)) {
    throw new Error("workflow meta must be a plain object literal");
  }
  const name = requiredString(value.name, "meta.name");
  const description = requiredString(value.description, "meta.description");
  const meta: WorkflowMeta = { name, description };
  if (value.whenToUse !== undefined) {
    meta.whenToUse = requiredString(value.whenToUse, "meta.whenToUse");
  }
  if (value.phases !== undefined) {
    if (!Array.isArray(value.phases)) {
      throw new Error("meta.phases must be an array");
    }
    meta.phases = value.phases.map((phase, index) => {
      if (!isPlainRecord(phase)) {
        throw new Error(`meta.phases[${index}] must be a plain object`);
      }
      const result: NonNullable<WorkflowMeta["phases"]>[number] = {
        title: requiredString(phase.title, `meta.phases[${index}].title`),
      };
      if (phase.detail !== undefined) {
        result.detail = requiredString(phase.detail, `meta.phases[${index}].detail`);
      }
      if (phase.model !== undefined) {
        result.model = requiredString(phase.model, `meta.phases[${index}].model`);
      }
      return result;
    });
  }
  const allowed = new Set(["name", "description", "whenToUse", "phases"]);
  for (const key of Object.keys(value)) {
    if (!allowed.has(key)) {
      throw new Error(`unsupported workflow meta field: ${key}`);
    }
  }
  return meta;
}

function evaluateLiteral(node: AstNode | null | undefined, context: string): unknown {
  if (!node) {
    throw new Error(`${context} must be a static literal`);
  }
  if (node.type === "Literal") {
    const value = node.value;
    if (["string", "number", "boolean"].includes(typeof value) || value === null) {
      return value;
    }
  }
  if (node.type === "TemplateLiteral") {
    const expressions = node.expressions as AstNode[];
    const quasis = node.quasis as Array<AstNode & { value: { cooked?: string } }>;
    if (expressions.length === 0) {
      return quasis[0]?.value.cooked ?? "";
    }
  }
  if (node.type === "UnaryExpression" && node.operator === "-" && (node.argument as AstNode).type === "Literal") {
    const value = (node.argument as AstNode).value;
    if (typeof value === "number") {
      return -value;
    }
  }
  if (node.type === "ArrayExpression") {
    const elements = node.elements as Array<AstNode | null>;
    if (elements.some((element) => element === null)) {
      throw new Error(`${context} must not contain sparse arrays`);
    }
    return elements.map((element, index) => {
      if (element?.type === "SpreadElement") {
        throw new Error(`${context} must not contain array spread`);
      }
      return evaluateLiteral(element, `${context}[${index}]`);
    });
  }
  if (node.type === "ObjectExpression") {
    const result: Record<string, unknown> = Object.create(null) as Record<string, unknown>;
    for (const property of node.properties as AstNode[]) {
      if (property.type === "SpreadElement") {
        throw new Error(`${context} must not contain object spread`);
      }
      if (property.type !== "Property" || property.kind !== "init" || property.method === true || property.computed === true) {
        throw new Error(`${context} must contain only plain object properties`);
      }
      const keyNode = property.key as AstNode;
      const key = keyNode.type === "Identifier" ? String(keyNode.name) : String(keyNode.value);
      if (reservedKeys.has(key)) {
        throw new Error(`${context} contains reserved key: ${key}`);
      }
      result[key] = evaluateLiteral(property.value as AstNode, `${context}.${key}`);
    }
    return result;
  }
  throw new Error(`${context} must be a static literal`);
}

function validateDeterministicScript(root: AstNode): void {
  walk(root, (node) => {
    if (node.type === "ImportDeclaration" || node.type === "ImportExpression") {
      throw new Error("workflow scripts must not use import");
    }
    if (node.type === "CallExpression") {
      const callee = node.callee as AstNode;
      if (callee.type === "Identifier" && callee.name === "require") {
        throw new Error("workflow scripts must not use require");
      }
      const member = staticMember(callee);
      if (member?.object === "Date" && member.property === "now") {
        throw new Error("workflow scripts must not use Date.now()");
      }
      if (member?.object === "Math" && member.property === "random") {
        throw new Error("workflow scripts must not use Math.random()");
      }
    }
    if (node.type === "NewExpression") {
      const callee = node.callee as AstNode;
      if (callee.type === "Identifier" && callee.name === "Date") {
        throw new Error("workflow scripts must not use new Date()");
      }
    }
  });
}

function staticMember(node: AstNode): { object: string; property: string } | null {
  if (node.type !== "MemberExpression") {
    return null;
  }
  const object = node.object as AstNode;
  if (object.type !== "Identifier") {
    return null;
  }
  const property = staticString(node.property as AstNode);
  return property === null ? null : { object: String(object.name), property };
}

function staticString(node: AstNode): string | null {
  if (node.type === "Identifier") {
    return String(node.name);
  }
  if (node.type === "Literal" && typeof node.value === "string") {
    return node.value;
  }
  if (node.type === "BinaryExpression" && node.operator === "+") {
    const left = staticString(node.left as AstNode);
    const right = staticString(node.right as AstNode);
    return left === null || right === null ? null : left + right;
  }
  return null;
}

function walk(node: AstNode, visit: (node: AstNode) => void): void {
  visit(node);
  for (const [key, value] of Object.entries(node)) {
    if (key === "start" || key === "end" || key === "loc") {
      continue;
    }
    if (Array.isArray(value)) {
      for (const item of value) {
        if (isNode(item)) {
          walk(item, visit);
        }
      }
    } else if (isNode(value)) {
      walk(value, visit);
    }
  }
}

function isNode(value: unknown): value is AstNode {
  return typeof value === "object" && value !== null && "type" in value && typeof value.type === "string";
}

function isPlainRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function requiredString(value: unknown, field: string): string {
  if (typeof value !== "string" || value.trim() === "") {
    throw new Error(`${field} must be a non-empty string`);
  }
  return value;
}
