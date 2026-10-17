import type { Automation, AutomationTemplate } from "../api/types";

const PLACEHOLDER = /\{\{\s*([a-zA-Z][a-zA-Z0-9_]*)\s*\}\}/g;

function escapeRegExp(s: string): string {
  return s.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
}

/**
 * The automation a template installs, its {{key}} filled in — as the gateway's
 * library.Template.Instantiate does: in the pattern a value is matched as
 * typed, an empty sender means any, and the name and description default to
 * the template's.
 */
export function instantiate(t: AutomationTemplate, values: Record<string, string>): Automation {
  const fill = (s: string | undefined, pattern = false) =>
    s?.replace(PLACEHOLDER, (_, key: string) => {
      const v = (values[key] ?? "").trim();
      return pattern ? escapeRegExp(v) : v;
    });
  const a = t.automation;
  return {
    ...a,
    id: "",
    enabled: true,
    name: fill(a.name) || t.name,
    description: fill(a.description) || t.description,
    pattern: fill(a.pattern, true),
    senders: a.senders?.map((s) => fill(s)!).filter((s) => s.trim() !== ""),
    conditions: a.conditions?.map((c) => ({ ...c, value: fill(c.value)! })),
    actions: a.actions.map((act) => ({
      ...act,
      to: fill(act.to),
      text: fill(act.text),
      url: fill(act.url),
      body: fill(act.body),
      code: fill(act.code),
    })),
  };
}

/** Required params left empty, by label. */
export function missing(t: AutomationTemplate, values: Record<string, string>): string[] {
  return (t.params ?? []).filter((p) => p.required && !(values[p.key] ?? "").trim()).map((p) => p.label);
}
