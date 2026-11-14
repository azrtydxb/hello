// Packages the extension: node tools/build.mjs <outdir>
//   <outdir>/hello-extension.zip   always; loads unpacked or on the Web Store
//   <outdir>/hello-extension.crx   when a signing key is given
//   <outdir>/hello-extension.json  version, id and the sha256 of each file
// The signing key is HELLO_EXTENSION_KEY_FILE (a path) or HELLO_EXTENSION_KEY
// (the PEM). It must be the private half of the manifest's "key", or the id
// would change; the build refuses a key that is not.
import { createHash } from "node:crypto";
import { mkdirSync, readFileSync, readdirSync, writeFileSync } from "node:fs";
import { dirname, join, relative } from "node:path";
import { fileURLToPath } from "node:url";
import { crx3, extensionId, publicKeyOf } from "./crx.mjs";
import { zip } from "./zip.mjs";

const root = join(dirname(fileURLToPath(import.meta.url)), "..");
const outDir = process.argv[2];
if (!outDir) {
  console.error("usage: node tools/build.mjs <outdir>");
  process.exit(2);
}

const INCLUDE =
  /^(manifest\.json|[a-z]+\.(js|html|css)|lib\/[a-z]+\.js|icons\/\d+\.png)$/;
function walk(dir) {
  return readdirSync(dir, { withFileTypes: true }).flatMap((e) =>
    e.isDirectory() ? walk(join(dir, e.name)) : [join(dir, e.name)],
  );
}
const files = walk(root)
  .map((p) => relative(root, p).split("\\").join("/"))
  .filter((n) => INCLUDE.test(n) && !n.endsWith(".test.js"))
  .sort()
  .map((name) => ({ name, data: readFileSync(join(root, name)) }));

const manifest = JSON.parse(readFileSync(join(root, "manifest.json"), "utf8"));
const id = extensionId(manifest.key);
const bytes = zip(files);
mkdirSync(outDir, { recursive: true });
writeFileSync(join(outDir, "hello-extension.zip"), bytes);

let pem = process.env.HELLO_EXTENSION_KEY ?? "";
if (process.env.HELLO_EXTENSION_KEY_FILE)
  pem = readFileSync(process.env.HELLO_EXTENSION_KEY_FILE, "utf8");
let signed = false;
if (pem.trim() !== "") {
  if (publicKeyOf(pem) !== manifest.key) {
    console.error(
      "the signing key is not the private half of the manifest key; the extension id would change",
    );
    process.exit(1);
  }
  writeFileSync(join(outDir, "hello-extension.crx"), crx3(bytes, pem));
  signed = true;
}

writeFileSync(
  join(outDir, "hello-extension.json"),
  JSON.stringify(
    {
      version: manifest.version,
      id,
      signed,
      zipSha256: createHash("sha256").update(bytes).digest("hex"),
    },
    null,
    2,
  ) + "\n",
);
console.log(
  `hello-extension ${manifest.version} id ${id} ${signed ? "(signed crx)" : "(no signing key: zip only)"}`,
);
