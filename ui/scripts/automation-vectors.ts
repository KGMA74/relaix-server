// Runs the shared automation cases (server/policy/testdata) against the
// dashboard's matcher, so the tester shows what the gateway and the phone do.
//   npm run test:automations
import { readFileSync } from "node:fs";
import { isJsonTemplate, match, numbersOf, render, renderJson } from "../src/lib/automation.ts";

const file = JSON.parse(readFileSync(new URL("../../policy/testdata/automation_vectors.json", import.meta.url), "utf8"));
let failed = 0;
for (const c of file.cases) {
  const r = match({ id: "x", name: "x", actions: [], ...c.automation }, c.sms);
  const fieldsOk = Object.entries(c.fields ?? {}).every(([k, v]) => r.fields[k] === v);
  const renderOk = !c.template || render(c.template, r.fields) === c.rendered;
  const jsonOk = !c.jsonTemplate || (isJsonTemplate(c.jsonTemplate) && renderJson(c.jsonTemplate, r.fields) === c.jsonRendered);
  // The vectors hold numbers as JSON literals; the dashboard shows them parsed.
  const want = Object.fromEntries(Object.entries((c.numbers ?? {}) as Record<string, string>).map(([k, v]) => [k, Number(v)]));
  const numbersOk = !c.numbers || JSON.stringify(numbersOf(r.fields), Object.keys(want).sort()) === JSON.stringify(want, Object.keys(want).sort());
  if (r.matched !== c.match || !fieldsOk || !renderOk || !jsonOk || !numbersOk) {
    failed++;
    console.error(`FAIL ${c.name}`, r, c.jsonTemplate ? renderJson(c.jsonTemplate, r.fields) : "", numbersOf(r.fields));
  }
}
console.log(failed ? `${failed} case(s) failed` : `${file.cases.length} automation cases pass`);
process.exit(failed ? 1 : 0);
