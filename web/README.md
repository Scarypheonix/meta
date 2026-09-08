# web — the Origin playground

A static page that compiles and runs Origin in the visitor's browser. No server executes user
code, and there is no backend of any kind: this directory is files, served as files.

`docs/spec/playground-runtime.md` is normative for what the module does. ADR-0032 (both
engines, the VM by default), ADR-0033 (a host with no filesystem), ADR-0034 (CodeMirror,
vendored), ADR-0035 (sharing through the URL fragment) and ADR-0036 (a worker, chunked and
bounded output) are the decisions behind the rest.

## What is here

| File | What it is | Committed |
|---|---|---|
| `index.html` | the page | yes |
| `style.css` | the page's design, light and dark | yes |
| `app.js` | the shell: editor, running, sharing, remembering | yes |
| `worker.js` | holds the module and runs one program per message (ADR-0036) | yes |
| `origin-mode.js` | Origin syntax highlighting, written against spec/01-lexical.md | yes |
| `share.js` | the fragment codec (ADR-0035) | yes |
| `examples.js` | **generated** from `tests/e2e/cases` — do not edit | yes |
| `codemirror.js` | **vendored** CodeMirror 6 bundle (ADR-0034) | yes |
| `codemirror-LICENSE.txt` | its licences and exact versions | yes |
| `wasm_exec.js` | Go's, verbatim, pinned to the toolchain below | yes |
| `origin.wasm` | the compiler and both engines | no — built |

## Building

```
GOOS=js GOARCH=wasm go build -o web/origin.wasm ./cmd/originwasm
```

`origin.wasm` is not committed: it is ~6 MB, it is reproducible from source in that one
command, and a copy per rebuild would be history nobody can read.

`wasm_exec.js` **is** committed, and it is Go's, copied verbatim from
`$(go env GOROOT)/lib/wasm/wasm_exec.js`. It is version-locked to the toolchain that builds
the module — **go1.24.7** — because the JavaScript side and the module's ABI change together.
`tests/web` fails if the committed copy and the current toolchain's have drifted, so this is
checked rather than remembered.

## Running it locally

Any static file server over this directory. The module is fetched and instantiated from an
`ArrayBuffer` rather than with `instantiateStreaming`, so a server that does not send
`application/wasm` still works.

```
python3 -m http.server 8000 --directory web
```

## Regenerating the examples

`examples.js` is the worked examples of `docs/spec/10-examples.md`, taken from the end-to-end
cases derived from it, so every program the playground offers is one the whole suite already
runs on three engines at three optimization levels. It is generated, and `tests/web` fails
when it is stale:

```
UPDATE_GOLDEN=1 go test ./tests/web/
```

## Rebuilding the CodeMirror bundle

Vendored rather than loaded from a CDN (ADR-0034): a CDN is a third party who can see every
visitor, and this page's claim is that there is not one. The bundle is 290 KB, 94 KB
compressed — against the module's ~1.5 MB, which is the trade that decision was made on.

```
mkdir cm && cd cm && npm init -y
npm install @codemirror/state@6.5.2 @codemirror/view@6.36.4 \
            @codemirror/commands@6.8.0 @codemirror/language@6.10.8 \
            @lezer/highlight@1.2.1
# entry.js re-exports only what web/app.js uses; its contents are in the header of
# web/codemirror.js's source entry, reproduced in codemirror-LICENSE.txt's version list.
npx esbuild entry.js --bundle --format=iife --global-name=CM --minify \
    --target=es2020 --legal-comments=none --outfile=../web/codemirror.js
```

Exact versions and licences are in `codemirror-LICENSE.txt`. Updating the bundle is a
deliberate commit, the way `bootstrap/` is: a binary nobody can reproduce is not a dependency,
it is a mystery.

## Deploying

Copy this directory to any static host after building the module. There is nothing else —
no runtime, no database, no configuration. Note that `site/` (ADR-0002) is a different,
unrelated static site in this repository, so a host serving one of them needs its source
directory pointed at the right one.

## Tests

- `tests/wasm` runs 98 of the 102 end-to-end cases through the module on both engines under
  Node and compares against the corpus's golden files.
- `tests/web` drives this page in Chromium: every example on both engines against the same
  golden files, a shared link round-tripping, the syntax mode highlighting, and the page
  still running Origin with the editor bundle blocked. It skips when node or playwright is
  absent rather than passing quietly.
