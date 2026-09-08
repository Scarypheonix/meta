// The worker that runs Origin programs (ADR-0036).
//
// It holds the WebAssembly module and nothing about the page; the page holds a handle to it
// and nothing else. That split is what makes `terminate()` a stop button that works on a
// program which has stopped yielding -- the case a cooperative check could not have caught.

importScripts("wasm_exec.js");

let ready = null;

// boot instantiates the module once and resolves when `originRun` is reachable.
//
// `go.run` is deliberately not awaited: Origin's host blocks in `select {}` so that the
// exported function outlives startup, so the promise `go.run` returns settles only when the
// module exits. What it does do synchronously is run Go's main far enough to export the
// function, which is why waiting for the global is the right readiness signal.
function boot() {
  if (ready) return ready;
  ready = (async () => {
    const go = new Go();
    // fetch + instantiate rather than instantiateStreaming: streaming refuses a response
    // that is not served as application/wasm, and this has to work on any static host.
    const bytes = await (await fetch("origin.wasm")).arrayBuffer();
    const { instance } = await WebAssembly.instantiate(bytes, go.importObject);
    go.run(instance);
    if (typeof originRun !== "function") {
      throw new Error("the module loaded but did not export originRun");
    }
  })();
  return ready;
}

self.onmessage = async (e) => {
  const req = e.data;
  try {
    await boot();
  } catch (err) {
    // The module failing to load is the host's own error channel, never the program's
    // (spec §7). It must not be reported as though the program failed.
    self.postMessage({ kind: "failed", message: String(err && err.message ? err.message : err) });
    return;
  }

  let result;
  try {
    result = originRun({
      source: req.source,
      engine: req.engine || "vm",
      opt: req.opt === undefined ? 1 : req.opt,
      args: req.args || [],
      // Chunks are delivered as the program produces them, so a program that prints and
      // then never returns still shows what it printed.
      onOutput: (stream, text) => self.postMessage({ kind: "output", stream, text }),
    });
  } catch (err) {
    self.postMessage({ kind: "failed", message: String(err && err.message ? err.message : err) });
    return;
  }

  self.postMessage({ kind: "done", ...result });
};
