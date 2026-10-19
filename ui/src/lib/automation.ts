import type { Automation, AutomationCondition } from "../api/types";

// Mirrors server/policy/automation.go — the reference — and the agent's
// engine, so the tester shows what the phone will do. The shared cases live
// in server/policy/testdata/automation_vectors.json.

export const BUILTINS = ["sender", "body", "receivedAt", "sim"] as const;
export const PIN = "pin";

// What a template fills between braces: a field name, or a calculation.
// Anything else in braces ({1}, { }) stays as written.
const TOKEN = /\{([A-Za-z][A-Za-z0-9]*|[A-Za-z0-9. ()+*/-]+)\}/g;
const FIELD = /^[A-Za-z][A-Za-z0-9]*$/;
const GROUP = /\(\?P?<([^>]*)>/g;

/** Builtins, then the pattern's named groups. */
export function fieldsOf(pattern?: string): string[] {
  const out: string[] = [...BUILTINS];
  for (const m of (pattern ?? "").matchAll(GROUP)) if (m[1] && !out.includes(m[1])) out.push(m[1]);
  return out;
}

/** The pattern as a JS RegExp: (?P<x>) becomes (?<x>), a leading (?i) a flag. */
export function compile(pattern: string): RegExp {
  let flags = "";
  let p = pattern.replace(/\(\?P</g, "(?<");
  if (p.startsWith("(?i)")) {
    flags = "i";
    p = p.slice(4);
  }
  return new RegExp(p, flags);
}

export function patternError(pattern?: string): string | null {
  if (!pattern) return null;
  try {
    compile(pattern);
  } catch (e) {
    return e instanceof Error ? e.message : "expression invalide";
  }
  for (const m of pattern.matchAll(GROUP)) {
    const name = m[1] ?? "";
    if (!/^[A-Za-z][A-Za-z0-9]*$/.test(name) || (BUILTINS as readonly string[]).includes(name) || name === PIN) {
      return `nom de groupe « ${name} » : une lettre puis lettres ou chiffres, et pas ${BUILTINS.join(", ")} ou pin`;
    }
  }
  return null;
}

/** "5 000" → 5000, "10,25" → 10.25. */
export function toNumber(s: string): number | null {
  const n = Number(s.replace(/[\s\u00a0\u202f]/g, "").replace(",", "."));
  return s.trim() !== "" && Number.isFinite(n) ? n : null;
}

function holds(c: AutomationCondition, v: string): boolean {
  switch (c.op) {
    case "eq":
      return v.toLowerCase() === c.value.toLowerCase();
    case "ne":
      return v.toLowerCase() !== c.value.toLowerCase();
    case "contains":
      return v.toLowerCase().includes(c.value.toLowerCase());
  }
  const got = toNumber(v);
  const want = toNumber(c.value);
  if (got === null || want === null) return false;
  switch (c.op) {
    case "gt":
      return got > want;
    case "gte":
      return got >= want;
    case "lt":
      return got < want;
    case "lte":
      return got <= want;
  }
  return false;
}

export interface Sms {
  sender: string;
  body: string;
  receivedAt: string;
  sim: number;
}

export interface MatchResult {
  matched: boolean;
  fields: Record<string, string>;
  /** Why it did not match, for the tester. */
  reason?: string;
}

export function match(a: Automation, sms: Sms): MatchResult {
  const fields: Record<string, string> = { sender: sms.sender, body: sms.body, receivedAt: sms.receivedAt, sim: String(sms.sim) };
  if (!a.enabled) return { matched: false, fields, reason: "désactivée" };
  if (a.subscriptionId && a.subscriptionId !== sms.sim) return { matched: false, fields, reason: "autre SIM" };
  if (a.senders?.length && !a.senders.some((s) => s.toLowerCase() === sms.sender.trim().toLowerCase())) {
    return { matched: false, fields, reason: "expéditeur non listé" };
  }
  if (a.pattern) {
    let m: RegExpExecArray | null;
    try {
      m = compile(a.pattern).exec(sms.body);
    } catch {
      return { matched: false, fields, reason: "expression invalide" };
    }
    if (!m) return { matched: false, fields, reason: "le motif ne se trouve pas dans le message" };
    for (const [k, v] of Object.entries(m.groups ?? {})) fields[k] = v ?? "";
  }
  for (const c of a.conditions ?? []) {
    if (!holds(c, fields[c.field] ?? "")) return { matched: false, fields, reason: `condition ${c.field} ${c.op} ${c.value} fausse` };
  }
  return { matched: true, fields };
}

/** A calculation in braces: not a field name, and an operator in it. */
export function isExpression(inner: string): boolean {
  return !FIELD.test(inner) && /[+\-*/]/.test(inner);
}

type Expr = (get: (name: string) => number) => number;

/**
 * Parses a calculation — fields, numbers, + - * / and parentheses — as
 * policy/expr.go does, and lists the fields it uses. Null when it does not
 * parse.
 */
export function parseExpr(s: string): { eval: Expr; names: string[] } | null {
  if (s.length > 100) return null;
  let i = 0;
  const names: string[] = [];
  let bad = false;
  const peek = () => {
    while (s[i] === " ") i++;
    return s[i] ?? "";
  };
  const sum = (): Expr => {
    let left = product();
    for (;;) {
      const op = peek();
      if (bad || (op !== "+" && op !== "-")) return left;
      i++;
      const l = left;
      const r = product();
      left = op === "+" ? (g) => l(g) + r(g) : (g) => l(g) - r(g);
    }
  };
  const product = (): Expr => {
    let left = unary();
    for (;;) {
      const op = peek();
      if (bad || (op !== "*" && op !== "/")) return left;
      i++;
      const l = left;
      const r = unary();
      left =
        op === "*"
          ? (g) => l(g) * r(g)
          : (g) => {
              const a = l(g);
              const b = r(g);
              if (b === 0) throw new Error("division by zero");
              return a / b;
            };
    }
  };
  const unary = (): Expr => {
    if (peek() === "-") {
      i++;
      const e = unary();
      return (g) => -e(g);
    }
    return atom();
  };
  const atom = (): Expr => {
    const c = peek();
    const start = i;
    if (c === "(") {
      i++;
      const e = sum();
      if (peek() !== ")") bad = true;
      i++;
      return e;
    }
    if (/[0-9]/.test(c)) {
      while (/[0-9.]/.test(s[i] ?? "")) i++;
      const lit = s.slice(start, i);
      const v = Number(lit);
      if (!/^[0-9]+(\.[0-9]*)?$/.test(lit) || !Number.isFinite(v)) bad = true;
      return () => v;
    }
    if (/[A-Za-z]/.test(c)) {
      while (/[A-Za-z0-9]/.test(s[i] ?? "")) i++;
      const name = s.slice(start, i);
      names.push(name);
      return (g) => g(name);
    }
    bad = true;
    return () => 0;
  };
  const e = sum();
  peek();
  if (bad || i !== s.length) return null;
  return { eval: e, names };
}

/** Rounds half away from zero, as the gateway and the phone do. */
function roundTo(v: number, places: number): number {
  const p = 10 ** places;
  const r = Math.floor(Math.abs(v) * p + 0.5) / p;
  return v < 0 ? -r : r;
}

/** The shortest decimal: 400, 66.67. */
export function formatNumber(v: number): string {
  return v === 0 ? "0" : String(v);
}

/** A calculation's result rounded to places, or null when it cannot be computed. */
export function evalExpr(s: string, fields: Record<string, string>, places: number): string | null {
  const e = parseExpr(s);
  if (!e) return null;
  try {
    const v = e.eval((name) => {
      const n = toNumber(fields[name] ?? "");
      if (n === null) throw new Error("not a number");
      return n;
    });
    return Number.isFinite(v) ? formatNumber(roundTo(v, places)) : null;
  } catch {
    return null;
  }
}

/** What a template puts for the token inner; undefined when it is not a token. */
function fill(inner: string, fields: Record<string, string>, places: number): { value: string; computed: boolean; ok: boolean } | undefined {
  if (FIELD.test(inner)) return { value: fields[inner] ?? "", computed: false, ok: true };
  if (!isExpression(inner)) return undefined;
  const v = evalExpr(inner, fields, places);
  return { value: v ?? "", computed: true, ok: v !== null };
}

/** Fills {name} and {calculations}; {pin} is shown masked — only the phone knows it. */
export function render(template: string, fields: Record<string, string>): string {
  return template.replace(TOKEN, (m, inner: string) => {
    if (inner === PIN) return "••••";
    return fill(inner, fields, 2)?.value ?? m;
  });
}

/**
 * Fills a USSD code as the phone does (AutomationRules.renderUssd): no
 * spaces, and an amount with zero decimals as a whole number \u2014 "200.0" is
 * 200, "5 000,00" is 5000. Left in, a decimal point is dropped on the way
 * and 200.0 reaches the network as 2000.
 */
export function renderUssd(template: string, fields: Record<string, string>): string {
  return template.replace(TOKEN, (m, inner: string) => {
    if (inner === PIN) return m; // the phone's vault fills it
    const f = fill(inner, fields, 0);
    if (!f) return m;
    // A calculation is rounded to a whole number; one that cannot be computed
    // leaves "?", which isDialable refuses.
    if (!f.ok) return "?";
    if (f.computed) return f.value;
    const c = f.value.replace(/[\s\u00a0\u202f]/g, "");
    return /^([0-9]+)[.,]0+$/.exec(c)?.[1] ?? c;
  });
}

/**
 * Whether a filled USSD code can be dialled as it is: the phone refuses
 * anything but digits, * and # (and {pin}) rather than send another amount.
 */
export function isDialable(code: string): boolean {
  return /^[*#]([0-9*#]|\{pin\})*#$/.test(code);
}

/**
 * A webhook body meant as JSON: an object or an array. "{amount}" alone is a
 * field, not an object — an object opens on a quote or closes at once.
 */
export function isJsonTemplate(template: string): boolean {
  const t = template.replace(/^[ \t\r\n]+/, "");
  if (t.startsWith("[")) return true;
  if (!t.startsWith("{")) return false;
  const rest = t.slice(1).replace(/^[ \t\r\n]+/, "");
  return rest.startsWith('"') || rest.startsWith("}");
}

const TOKEN_AT = /^\{([A-Za-z][A-Za-z0-9]*|[A-Za-z0-9. ()+*/-]+)\}/;
const JSON_NUMBER = /^-?(0|[1-9][0-9]*)(\.[0-9]+)?$/;

/** v as a JSON number literal when it reads as an amount; not "+226…" or "0701…". */
export function jsonNumber(v: string): string | null {
  const s = v.replace(/[   ]/g, "").replace(/,/g, ".");
  return JSON_NUMBER.test(s) ? s : null;
}

/** v as the inside of a JSON string literal — the same bytes as the gateway and the phone. */
export function escapeJson(v: string): string {
  let out = "";
  for (const ch of v) {
    const c = ch.codePointAt(0) ?? 0;
    if (ch === '"') out += '\\"';
    else if (ch === "\\") out += "\\\\";
    else if (ch === "\n") out += "\\n";
    else if (ch === "\r") out += "\\r";
    else if (ch === "\t") out += "\\t";
    else if (ch === "\b") out += "\\b";
    else if (ch === "\f") out += "\\f";
    else if (c < 0x20) out += "\\u" + c.toString(16).padStart(4, "0");
    else out += ch;
  }
  return out;
}

function jsonValue(v: string): string {
  if (v === "") return "null";
  return jsonNumber(v) ?? `"${escapeJson(v)}"`;
}

/**
 * Fills a JSON body so it stays JSON: a field inside quotes is escaped there,
 * a bare one is a value — a number when it reads as one, null when empty,
 * else a string. Same rules as policy.RenderJSON.
 */
export function renderJson(template: string, fields: Record<string, string>): string {
  let out = "";
  let inString = false;
  for (let i = 0; i < template.length; ) {
    const c = template[i];
    if (inString && c === "\\" && i + 1 < template.length) {
      out += template.slice(i, i + 2);
      i += 2;
      continue;
    }
    if (c === '"') inString = !inString;
    else if (c === "{") {
      const m = TOKEN_AT.exec(template.slice(i));
      const f = m ? fill(m[1] ?? "", fields, 2) : undefined;
      if (m && f) {
        // A calculation is a number, or null when it cannot be computed.
        out += inString ? escapeJson(f.value) : f.ok ? jsonValue(f.value) : "null";
        i += m[0].length;
        continue;
      }
    }
    out += c;
    i++;
  }
  return out;
}

/** A webhook body as the phone will post it: JSON-aware when it is JSON. */
export function renderBody(template: string, fields: Record<string, string>): string {
  return isJsonTemplate(template) ? renderJson(template, fields) : render(template, fields);
}

/** Why a JSON body would not be JSON once filled in, or null. */
export function jsonBodyError(template: string | undefined, known: string[]): string | null {
  if (!template || !isJsonTemplate(template)) return null;
  try {
    JSON.parse(renderJson(template, Object.fromEntries(known.map((k) => [k, "0"]))));
    return null;
  } catch {
    return 'JSON invalide une fois rempli — un champ texte entre guillemets ("{from}"), un nombre sans ({amount}).';
  }
}

/** The fields that read as numbers, as the webhook's "numbers" carries them. */
export function numbersOf(fields: Record<string, string>): Record<string, number> {
  const out: Record<string, number> = {};
  for (const [k, v] of Object.entries(fields)) {
    if ((BUILTINS as readonly string[]).includes(k)) continue;
    const n = jsonNumber(v);
    if (n !== null) out[k] = Number(n);
  }
  return out;
}

/** Placeholders a template uses that the automation does not have. */
export function unknownFields(template: string | undefined, known: string[], allowPin = false): string[] {
  const out: string[] = [];
  for (const m of (template ?? "").matchAll(TOKEN)) {
    const inner = m[1] ?? "";
    if (inner === PIN && allowPin) continue;
    const names = isExpression(inner) ? (parseExpr(inner)?.names ?? []) : FIELD.test(inner) ? [inner] : [];
    for (const name of names) if (!known.includes(name) && !out.includes(name)) out.push(name);
  }
  return out;
}

/** Calculations a template holds that do not parse, as written. */
export function badCalculations(template: string | undefined): string[] {
  return [...(template ?? "").matchAll(TOKEN)].map((m) => m[1] ?? "").filter((inner) => isExpression(inner) && !parseExpr(inner));
}
