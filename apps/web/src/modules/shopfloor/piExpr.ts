// The step expression language of the PI step library (see services/shopfloor/internal/domain/expr.go, which the service
// uses to check a sheet when it is saved). A calculation computes a number from the values of earlier steps and a branch
// decides, from the same values, whether the kiosk jumps ahead. The text is parsed by a small recursive-descent parser;
// there is no eval and no function the parser does not list. Types are checked when the text is parsed.
//
//   or      := and { ("||" | "or") and }          and := not { ("&&" | "and") not }      not := ("!" | "not") not | compare
//   compare := sum [ ("<" | "<=" | ">" | ">=" | "==" | "!=") sum ]      sum := product { ("+" | "-") product }
//   product := unary { ("*" | "/") unary }        unary := "-" unary | primary
//   primary := number | name | name "(" [ or { "," or } ] ")" | "(" or ")"
//
// Functions: abs, min, max, round(x, places), floor, ceil. Names are the `var` of earlier steps.

import { Dec, MAX_ROUND_PLACES } from "./piDecimal";

export const MAX_EXPR_LENGTH = 500;
const MAX_NODES = 200;
const MAX_DEPTH = 32;

export type ExprType = "number" | "boolean";
/** A number result is decimal text, as the service prints it. */
export type ExprResult = { type: "number"; value: string } | { type: "boolean"; value: boolean };

type Node =
  | { k: "num"; v: Dec }
  | { k: "name"; name: string }
  | { k: "not" | "neg"; x: Node }
  | { k: "bin"; op: string; l: Node; r: Node }
  | { k: "call"; fn: string; args: Node[] };

export interface ParsedExpr {
  type: ExprType;
  /** The names the expression reads, sorted and without repeats. */
  names: string[];
  /** vars are decimal text by name. */
  eval(vars: Readonly<Record<string, string>>): ExprResult;
}

/** An evaluation that could not finish: a name without a value, or a division by zero. */
export class ExprEvalError extends Error {
  constructor(
    message: string,
    readonly missing?: string,
  ) {
    super(message);
  }
}

export const EXPR_FUNCTIONS: Record<string, number> = { abs: 1, floor: 1, ceil: 1, round: 2, min: -1, max: -1 };

interface Token {
  kind: "num" | "name" | "op" | "end";
  text: string;
  at: number;
}

function lex(src: string): Token[] {
  const out: Token[] = [];
  let i = 0;
  while (i < src.length) {
    const c = src[i] as string;
    if (/\s/.test(c)) {
      i++;
    } else if (/[0-9.]/.test(c)) {
      let j = i;
      while (j < src.length && /[0-9.]/.test(src[j] as string)) j++;
      const text = src.slice(i, j);
      if (text.split(".").length > 2 || text === "." || text.length > 30) throw new Error(`position ${i + 1}: "${text}" is not a number`);
      out.push({ kind: "num", text, at: i });
      i = j;
    } else if (/[a-z_]/.test(c)) {
      let j = i;
      while (j < src.length && /[a-z0-9_]/.test(src[j] as string)) j++;
      const word = src.slice(i, j);
      const alias = new Map([["and", "&&"], ["or", "||"], ["not", "!"]]);
      const op = alias.get(word);
      out.push(op ? { kind: "op", text: op, at: i } : { kind: "name", text: word, at: i });
      i = j;
    } else {
      const two = src.slice(i, i + 2);
      if (["<=", ">=", "==", "!=", "&&", "||"].includes(two)) {
        out.push({ kind: "op", text: two, at: i });
        i += 2;
      } else if ("+-*/()<>!,".includes(c)) {
        out.push({ kind: "op", text: c, at: i });
        i++;
      } else {
        throw new Error(`position ${i + 1}: "${c}" is not allowed in an expression`);
      }
    }
  }
  out.push({ kind: "end", text: "", at: src.length });
  return out;
}

const typeText = (t: ExprType) => (t === "boolean" ? "a true or false condition" : "a number");

class Parser {
  pos = 0;
  nodes = 0;
  depth = 0;
  names = new Set<string>();
  constructor(readonly toks: Token[]) {}

  peek() {
    return this.toks[this.pos] as Token;
  }
  next() {
    return this.toks[this.pos++] as Token;
  }
  isOp(...ops: string[]) {
    const t = this.peek();
    return t.kind === "op" && ops.includes(t.text);
  }
  grow() {
    if (++this.nodes > MAX_NODES) throw new Error(`the expression is too long: at most ${MAX_NODES} terms and operators`);
  }
  enter() {
    if (++this.depth > MAX_DEPTH) throw new Error(`the expression nests too deeply: at most ${MAX_DEPTH} levels`);
  }
  need(want: ExprType, got: ExprType, op: string, at: number) {
    if (want !== got) throw new Error(`position ${at + 1}: ${op} needs ${typeText(want)}, not ${typeText(got)}`);
  }

  chain(sub: () => [Node, ExprType], ops: string[], operand: ExprType, name: (op: string) => string): [Node, ExprType] {
    let [l, lt] = sub();
    while (this.isOp(...ops)) {
      const op = this.next();
      this.need(operand, lt, name(op.text), op.at);
      const [r, rt] = sub();
      this.need(operand, rt, name(op.text), op.at);
      l = { k: "bin", op: op.text, l, r };
      lt = operand;
      this.grow();
    }
    return [l, lt];
  }

  or(): [Node, ExprType] {
    this.enter();
    try {
      return this.chain(() => this.and(), ["||"], "boolean", () => "or");
    } finally {
      this.depth--;
    }
  }
  and(): [Node, ExprType] {
    return this.chain(() => this.not(), ["&&"], "boolean", () => "and");
  }
  not(): [Node, ExprType] {
    if (!this.isOp("!")) return this.compare();
    const op = this.next();
    this.enter();
    try {
      const [x, xt] = this.not();
      this.need("boolean", xt, "not", op.at);
      this.grow();
      return [{ k: "not", x }, "boolean"];
    } finally {
      this.depth--;
    }
  }
  compare(): [Node, ExprType] {
    const [l, lt] = this.sum();
    if (!this.isOp("<", "<=", ">", ">=", "==", "!=")) return [l, lt];
    const op = this.next();
    this.need("number", lt, op.text, op.at);
    const [r, rt] = this.sum();
    this.need("number", rt, op.text, op.at);
    this.grow();
    return [{ k: "bin", op: op.text, l, r }, "boolean"];
  }
  sum(): [Node, ExprType] {
    return this.chain(() => this.product(), ["+", "-"], "number", (op) => op);
  }
  product(): [Node, ExprType] {
    return this.chain(() => this.unary(), ["*", "/"], "number", (op) => op);
  }
  unary(): [Node, ExprType] {
    if (!this.isOp("-")) return this.primary();
    const op = this.next();
    this.enter();
    try {
      const [x, xt] = this.unary();
      this.need("number", xt, "-", op.at);
      this.grow();
      return [{ k: "neg", x }, "number"];
    } finally {
      this.depth--;
    }
  }
  primary(): [Node, ExprType] {
    const t = this.next();
    if (t.kind === "num") {
      this.grow();
      return [{ k: "num", v: Dec.parse(t.text) as Dec }, "number"];
    }
    if (t.kind === "name" && this.isOp("(")) return this.call(t);
    if (t.kind === "name") {
      this.names.add(t.text);
      this.grow();
      return [{ k: "name", name: t.text }, "number"];
    }
    if (t.kind === "op" && t.text === "(") {
      this.enter();
      try {
        const inner = this.or();
        const close = this.next();
        if (close.kind !== "op" || close.text !== ")") throw new Error(`position ${close.at + 1}: a closing bracket is missing`);
        return inner;
      } finally {
        this.depth--;
      }
    }
    if (t.kind === "end") throw new Error("the expression ends too early: a value is missing");
    throw new Error(`position ${t.at + 1}: "${t.text}" is not expected here`);
  }
  call(name: Token): [Node, ExprType] {
    const arity = Object.hasOwn(EXPR_FUNCTIONS, name.text) ? EXPR_FUNCTIONS[name.text] : undefined;
    if (arity === undefined) throw new Error(`position ${name.at + 1}: ${name.text} is not a function; the functions are abs, min, max, round, floor and ceil`);
    this.next();
    const args: Node[] = [];
    while (!this.isOp(")")) {
      if (args.length > 0) {
        const comma = this.next();
        if (comma.kind !== "op" || comma.text !== ",") throw new Error(`position ${comma.at + 1}: a comma or closing bracket is expected`);
      }
      this.enter();
      const [a, at] = this.or();
      this.depth--;
      this.need("number", at, name.text, name.at);
      args.push(a);
      if (this.peek().kind === "end") throw new Error(`position ${this.peek().at + 1}: a closing bracket is missing`);
    }
    this.next();
    if ((arity >= 0 && args.length !== arity) || (arity < 0 && args.length === 0)) {
      throw new Error(`position ${name.at + 1}: ${name.text} takes ${arity === 1 ? "one value" : arity === 2 ? "two values" : "at least one value"}`);
    }
    if (name.text === "round") {
      const places = args[1] as Node;
      if (places.k === "neg" || (places.k === "num" && (!places.v.isInteger() || places.v.cmp(Dec.of(BigInt(MAX_ROUND_PLACES))) > 0))) {
        throw new Error(`position ${name.at + 1}: round takes 0 to ${MAX_ROUND_PLACES} decimal places`);
      }
    }
    this.grow();
    return [{ k: "call", fn: name.text, args }, "number"];
  }
}

function run(node: Node, vars: Readonly<Record<string, string>>): Dec | boolean {
  switch (node.k) {
    case "num":
      return node.v;
    case "name": {
      // Own properties only: "constructor" or "__proto__" are names without a value, not members of Object.
      const text = Object.hasOwn(vars, node.name) ? vars[node.name] : undefined;
      const v = text === undefined ? null : Dec.parse(text);
      if (v === null) throw new ExprEvalError(`the step value ${node.name} is not known`, node.name);
      return v;
    }
    case "neg":
      return (run(node.x, vars) as Dec).neg();
    case "not":
      return !(run(node.x, vars) as boolean);
    case "call": {
      const a = node.args.map((x) => run(x, vars) as Dec);
      const first = a[0] as Dec;
      switch (node.fn) {
        case "abs":
          return first.abs();
        case "floor":
          return first.floor();
        case "ceil":
          return first.ceil();
        case "round": {
          const places = a[1] as Dec;
          if (!places.isInteger() || places.cmp(Dec.of(0n)) < 0 || places.cmp(Dec.of(BigInt(MAX_ROUND_PLACES))) > 0) throw new ExprEvalError(`round takes 0 to ${MAX_ROUND_PLACES} decimal places`);
          return first.round(Number(places.n));
        }
        case "min":
          return a.reduce((m, x) => (x.cmp(m) < 0 ? x : m));
        default:
          return a.reduce((m, x) => (x.cmp(m) > 0 ? x : m));
      }
    }
    default: {
      const l = run(node.l, vars);
      if (node.op === "&&") return l ? run(node.r, vars) : false;
      if (node.op === "||") return l ? true : run(node.r, vars);
      const x = l as Dec;
      const y = run(node.r, vars) as Dec;
      switch (node.op) {
        case "+":
          return x.add(y);
        case "-":
          return x.sub(y);
        case "*":
          return x.mul(y);
        case "/":
          if (y.isZero()) throw new ExprEvalError("division by zero");
          return x.div(y);
        case "<":
          return x.cmp(y) < 0;
        case "<=":
          return x.cmp(y) <= 0;
        case ">":
          return x.cmp(y) > 0;
        case ">=":
          return x.cmp(y) >= 0;
        case "==":
          return x.cmp(y) === 0;
        default:
          return x.cmp(y) !== 0;
      }
    }
  }
}

/** Parses and type-checks an expression; throws an Error whose message names what is wrong. */
export function parseExpr(src: string): ParsedExpr {
  if (src.trim() === "") throw new Error("the expression is empty");
  if (src.length > MAX_EXPR_LENGTH) throw new Error(`the expression is too long: at most ${MAX_EXPR_LENGTH} characters`);
  const p = new Parser(lex(src));
  const [root, type] = p.or();
  const rest = p.peek();
  if (rest.kind !== "end") throw new Error(`position ${rest.at + 1}: "${rest.text}" is not expected here`);
  return {
    type,
    names: [...p.names].sort(),
    eval(vars) {
      const v = run(root, vars);
      return typeof v === "boolean" ? { type: "boolean", value: v } : { type: "number", value: v.toString() };
    },
  };
}

/** Checks an expression of the wanted type; returns the message to show, or null when it is fine. */
export function exprProblem(src: string, want: ExprType): string | null {
  try {
    const e = parseExpr(src);
    return e.type === want ? null : want === "number" ? "The expression must compute a number, not a true or false condition." : "The condition must be true or false, such as temp > 80.";
  } catch (error) {
    return error instanceof Error ? error.message : "The expression is not valid.";
  }
}
