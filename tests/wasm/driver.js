// driver.js runs a list of Origin programs through the browser build and reports what each
// one printed, so that Go can compare it against the same program's native run.
//
// Node hosts the module here rather than a browser, and that is deliberate: the WebAssembly
// is identical, the boundary is identical, and Node is what a five-minute test suite can
// drive 100 programs through. What Node cannot check is the page around it, which is why
// `TestFibonacciRunsInABrowser` exists beside this and drives Chromium.
//
// The module is instantiated ONCE and reused for every case. Instantiating per case would
// be six megabytes of compile per program and would put this test far outside the suite's
// budget; `originRun` is a pure function of its request, so one instance is the same
// coverage. Usage: node driver.js <origin.wasm> <manifest.json>

"use strict";

const fs = require("fs");
const path = require("path");

globalThis.require = require;
globalThis.fs = fs;
globalThis.path = path;
globalThis.TextEncoder = require("util").TextEncoder;
globalThis.TextDecoder = require("util").TextDecoder;
globalThis.performance ??= require("performance");
globalThis.crypto ??= require("crypto");

require(path.join(__dirname, "wasm_exec.js"));

async function main() {
  const [wasmPath, manifestPath] = process.argv.slice(2);
  if (!wasmPath || !manifestPath) {
    console.error("usage: node driver.js <origin.wasm> <manifest.json>");
    process.exit(2);
  }
  const manifest = JSON.parse(fs.readFileSync(manifestPath, "utf8"));

  const go = new Go();
  // Go's wasm shim caps argv and env together at 4 KB and refuses to start past it. This
  // process inherits a large environment, and the module wants neither, so it gets neither.
  go.argv = ["originwasm"];
  go.env = {};

  const { instance } = await WebAssembly.instantiate(fs.readFileSync(wasmPath), go.importObject);
  go.run(instance); // deliberately not awaited: the host blocks so its export outlives startup
  if (typeof originRun !== "function") {
    console.error("the module loaded but did not export originRun");
    process.exit(1);
  }

  const results = [];
  for (const c of manifest.cases) {
    let r;
    try {
      r = originRun({
        source: fs.readFileSync(c.path, "utf8"),
        // argv[0] and the name diagnostics render, both of which the native run takes from
        // the case's repository-relative path. Byte-for-byte comparison needs them equal.
        name: c.name,
        engine: c.engine,
        opt: c.opt,
        args: c.args || [],
        // The case's `.in` file, byte for byte. A case with none reads nothing, which is
        // what the native run of it does too (spec/18-input.md).
        stdin: c.stdin || "",
      });
    } catch (err) {
      r = { thrown: String((err && err.message) || err) };
    }
    results.push({ case: c.case, engine: c.engine, opt: c.opt, ...r });
    if (process.env.ORIGIN_WASM_VERBOSE) {
      console.error(`ran ${c.case} on the ${c.engine}`);
    }
  }
  fs.writeFileSync(manifest.out, JSON.stringify(results));

  // The module's main blocks in `select {}` so that its export outlives startup, which
  // means the Go runtime never exits and Node keeps an event handler pending forever.
  // Ending the process here is what the host has to do once it has its answers.
  process.exit(0);
}

main().catch((err) => {
  console.error(err);
  process.exit(1);
});
