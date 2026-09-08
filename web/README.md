# web — the Origin playground

A static page that compiles and runs Origin in the visitor's browser. No server executes
user code, and there is no backend of any kind: this directory is files, served as files.

`docs/spec/playground-runtime.md` is normative for what the module does.
ADR-0032 (both engines, the VM by default), ADR-0033 (a host with no filesystem),
ADR-0034 (CodeMirror, when the editor lands), ADR-0035 (sharing via the URL fragment) and
ADR-0036 (a worker, chunked and bounded output) are the decisions behind it.

## Building

```
GOOS=js GOARCH=wasm go build -o web/origin.wasm ./cmd/originwasm
```

`origin.wasm` is not committed — it is ~6 MB and reproducible from source in that one
command, so a copy per rebuild would be history nobody can read. `wasm_exec.js` **is**
committed, and it is Go's, copied verbatim from `$(go env GOROOT)/lib/wasm/wasm_exec.js`.
It is version-locked to the toolchain that builds the module: **go1.24.7**. Rebuilding with
a different Go means copying that file again from the same toolchain, because the JavaScript
side and the module's ABI change together.

## Running it locally

Any static file server over this directory. The module is fetched and instantiated from an
`ArrayBuffer` rather than with `instantiateStreaming`, so a server that does not send
`application/wasm` still works.

```
python3 -m http.server 8000 --directory web
```

## What is here

| File | What it is |
|---|---|
| `index.html` | the Phase 10 proof harness: a plain `<textarea>`, a Run button, a Stop button |
| `worker.js` | instantiates the module and runs one program per message (ADR-0036) |
| `wasm_exec.js` | Go's, verbatim, pinned to go1.24.7 |
| `origin.wasm` | built, not committed |

`index.html` is deliberately not the finished playground. It has no editor, no examples and
no sharing: it exists to prove the round trip end to end before anything visual is built on
top of it, and ADR-0034's editor replaces the textarea without touching the worker or the
module.
