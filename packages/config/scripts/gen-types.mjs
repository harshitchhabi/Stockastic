// Generates src/rulebook.generated.ts from rulebook.schema.json.
// Run: npm run gen -w @stockastic/config   (CI runs `gen` then fails on git diff)
import { compile } from "json-schema-to-typescript";
import { readFileSync, writeFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const root = join(dirname(fileURLToPath(import.meta.url)), "..");
const schema = JSON.parse(readFileSync(join(root, "rulebook.schema.json"), "utf8"));
const ts = await compile(schema, "Rulebook", {
  additionalProperties: false,
  bannerComment:
    "/* GENERATED from rulebook.schema.json by scripts/gen-types.mjs — do not edit by hand. */",
  style: { singleQuote: false },
});
writeFileSync(join(root, "src", "rulebook.generated.ts"), ts);
console.log("wrote src/rulebook.generated.ts");
