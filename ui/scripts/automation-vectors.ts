// Runs the shared automation cases (server/policy/testdata) against the
// dashboard's matcher, so the tester shows what the gateway and the phone do.
//   npm run test:automations
import { readFileSync } from "node:fs";
import { match, render } from "../src/lib/automation.ts";

const file = JSON.parse(readFileSync(new URL("../../policy/testdata/automation_vectors.json", import.meta.url), "utf8"));
let failed = 0;
for (const c of file.cases) {
  const r = match({ id: "x", name: "x", actions: [], ...c.automation }, c.sms);
  const fieldsOk = Object.entries(c.fields ?? {}).every(([k, v]) => r.fields[k] === v);
  const renderOk = !c.template || render(c.template, r.fields) === c.rendered;
  if (r.matched !== c.match || !fieldsOk || !renderOk) {
    failed++;
    console.error(`FAIL ${c.name}`, r);
  }
}
console.log(failed ? `${failed} case(s) failed` : `${file.cases.length} automation cases pass`);
process.exit(failed ? 1 : 0);
