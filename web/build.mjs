// esbuild build for the SolidJS client. Solid needs its dedicated JSX transform
// (not the generic automatic runtime), so we use esbuild-plugin-solid rather than
// the esbuild CLI. Output lands in web/dist, which the Go server serves as static
// files (FRONTEND_DIR). See context/frontend-architecture.md.
//
// `--mobile` injects window.__API_BASE_URL__ into dist/index.html so the
// Capacitor shell — which has no same-origin server to default to — talks to the
// atlas API over Tailscale. Override the URL with TIFL_MOBILE_API_BASE.
// See mobile/README.md.
import { build, context } from "esbuild";
import { solidPlugin } from "esbuild-plugin-solid";
import { mkdir, readFile, writeFile } from "node:fs/promises";

const mobile = process.argv.includes("--mobile");
const watch = process.argv.includes("--watch");
const mobileAPIBase = process.env.TIFL_MOBILE_API_BASE ?? "https://atlas.tail7e8149.ts.net:8443/api/v1";

const options = {
  entryPoints: ["src/main.tsx"],
  bundle: true,
  format: "esm",
  outfile: "dist/main.js",
  plugins: [solidPlugin()],
  sourcemap: true,
  minify: mobile || process.env.NODE_ENV === "production",
  logLevel: "info",
};

async function writeIndexHTML() {
  const marker = '<script type="module" src="/main.js"></script>';
  let html = await readFile("index.html", "utf8");
  if (!html.includes(marker)) {
    throw new Error("build.mjs: index.html is missing the main.js module script tag");
  }
  if (mobile) {
    const inject = `<script>window.__API_BASE_URL__ = ${JSON.stringify(mobileAPIBase)};</script>\n    ${marker}`;
    html = html.replace(marker, inject);
  }
  await writeFile("dist/index.html", html);
}

await mkdir("dist", { recursive: true });

if (watch) {
  const ctx = await context(options);
  await writeIndexHTML();
  await ctx.watch();
  console.log("watching for changes…");
} else {
  await build(options);
  await writeIndexHTML();
  console.log(mobile ? `built web/dist (mobile API base ${mobileAPIBase})` : "built web/dist");
}
