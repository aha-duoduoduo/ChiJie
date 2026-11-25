import { build } from "esbuild";
import { cp, mkdir, rm, readFile, writeFile } from "node:fs/promises";
import { basename, dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const root = dirname(dirname(fileURLToPath(import.meta.url)));
const dist = join(root, "dist");
const iconFiles = [
  "favicon.ico",
  "favicon-16x16.png",
  "favicon-32x32.png",
  "apple-touch-icon.png",
  "icon-48.png",
  "icon-192.png",
  "icon-512.png",
];

await rm(dist, { recursive: true, force: true });
await mkdir(dist, { recursive: true });
const result = await build({
  entryPoints: [join(root, "src/app.jsx")],
  outdir: dist,
  entryNames: "app-[hash]",
  metafile: true,
  bundle: true,
  minify: true,
  format: "esm",
  target: ["es2020"],
  define: { "process.env.NODE_ENV": '"production"' },
  logLevel: "info",
});
const entry = Object.entries(result.metafile.outputs).find(([, output]) => output.entryPoint);
if (!entry) throw new Error("Missing admin application entry point");
const html = await readFile(join(root, "index.html"), "utf8");
await writeFile(join(dist, "index.html"), html.replace('src="/app.js"', `src="/${basename(entry[0])}"`));
for (const file of iconFiles) {
  await cp(join(root, file), join(dist, file));
}

console.log("Built admin application to web/dist");
