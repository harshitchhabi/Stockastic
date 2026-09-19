// Validates rulebook.json against rulebook.schema.json, then checks the cross-field
// rules a schema can't express. The Go loader enforces the same rules at startup.
import Ajv from "ajv";
import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const root = join(dirname(fileURLToPath(import.meta.url)), "..");
const schema = JSON.parse(readFileSync(join(root, "rulebook.schema.json"), "utf8"));
const rb = JSON.parse(readFileSync(join(root, "rulebook.json"), "utf8"));

const ajv = new Ajv({ allErrors: true, strict: false });
if (!ajv.validate(schema, rb)) {
  console.error("rulebook.json fails schema validation:\n", ajv.errors);
  process.exit(1);
}

const errs = [];
const total = rb.event.timeline.reduce((s, b) => s + b.durationMin, 0);
if (total !== rb.event.totalMinutes) errs.push(`timeline sums to ${total}, expected ${rb.event.totalMinutes}`);
const wins = rb.event.timeline.map((b) => b.allocationWindow).filter((w) => w !== null);
if (JSON.stringify(wins) !== "[0,1,2,3]") errs.push(`allocation windows must be 0,1,2,3 in order, got ${wins}`);
const sum = (o) => Object.values(o).reduce((s, v) => s + (typeof v === "number" ? v : 0), 0);
const p1 = sum(rb.prizes.prize1), p4 = rb.prizes.prize4.riskAdjustedReturn + rb.prizes.prize4.drawdownControl + rb.prizes.prize4.diversification;
if (Math.abs(p1 - 1) > 1e-9) errs.push(`prize1 weights sum to ${p1}`);
if (Math.abs(p4 - 1) > 1e-9) errs.push(`prize4 weights sum to ${p4}`);
if (rb.qualification.qualifyingTeams !== 2 * rb.qualification.fundCount) errs.push("qualifyingTeams must be 2 x fundCount");
if (rb.teams.fundManagerSeats !== rb.qualification.fundCount * rb.teams.fundTeamSize) errs.push("fundManagerSeats must be fundCount x fundTeamSize");
const has = (path) => path.split(".").reduce((o, k) => (o != null && k in o ? o[k] : undefined), rb) !== undefined;
for (const p of Object.keys(rb.provenance)) if (!has(p)) errs.push(`provenance path does not exist: ${p}`);
if (errs.length) { console.error(errs.join("\n")); process.exit(1); }
console.log("rulebook.json OK");
