// esbuild build for the SolidJS client. Solid needs its dedicated JSX transform
// (not the generic automatic runtime), so we use esbuild-plugin-solid rather than
// the esbuild CLI. Output lands in web/dist, which the Go server serves as static
// files (FRONTEND_DIR). See context/frontend-architecture.md.
import { build, context } from "esbuild";
import { solidPlugin } from "esbuild-plugin-solid";
import { cp, mkdir } from "node:fs/promises";

const options = {
  entryPoints: ["src/main.tsx"],
  bundle: true,
  format: "esm",
  outfile: "dist/main.js",
  plugins: [solidPlugin()],
  sourcemap: true,
  minify: process.env.NODE_ENV === "production",
  logLevel: "info",
};

await mkdir("dist", { recursive: true });

if (process.argv.includes("--watch")) {
  const ctx = await context(options);
  await cp("index.html", "dist/index.html");
  await ctx.watch();
  console.log("watching for changes…");
} else {
  await build(options);
  await cp("index.html", "dist/index.html");
  console.log("built web/dist");
}
