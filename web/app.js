// The Origin playground's shell.
//
// It owns four things and delegates the rest: the editor (ADR-0034), running a program in a
// worker (ADR-0036), sharing through the fragment (ADR-0035), and remembering the visitor's
// program in their own browser and nowhere else.
//
// The editor is deliberately not on the critical path. index.html ships a real <textarea>,
// and this upgrades it to CodeMirror only if the bundle loaded; if it did not, everything
// else still works and the page says which editor it is using rather than pretending.

(function () {
  "use strict";

  const $ = (id) => document.getElementById(id);
  const els = {
    src: $("src"), run: $("run"), stop: $("stop"), share: $("share"),
    example: $("example"), engine: $("engine"), opt: $("opt"),
    stdin: $("stdin"),
    stdout: $("stdout"), stderr: $("stderr"), host: $("host"), status: $("status"),
    kind: $("editor-kind"), forget: $("forget"),
  };

  const STORE = {
    source: "origin.playground.source", engine: "origin.playground.engine",
    opt: "origin.playground.opt", stdin: "origin.playground.stdin",
  };

  // localStorage throws rather than returning null in some contexts -- a private window with
  // site data blocked, or an embedded view. The page must work with no storage at all, so
  // every access is guarded and a failure means "we cannot remember", never an error.
  const store = {
    get(k) { try { return localStorage.getItem(k); } catch (e) { return null; } },
    set(k, v) { try { localStorage.setItem(k, v); return true; } catch (e) { return false; } },
    remove(k) { try { localStorage.removeItem(k); } catch (e) { /* nothing to do */ } },
  };

  // ---------------------------------------------------------------- editor

  // editor is the one interface the rest of this file uses, so that CodeMirror's presence or
  // absence is a detail settled once, here.
  let editor = {
    get: () => els.src.value,
    set: (v) => { els.src.value = v; },
    focus: () => els.src.focus(),
  };

  function upgradeEditor() {
    if (typeof CM === "undefined" || typeof window.originMode !== "function") {
      els.kind.textContent = "plain editor";
      return;
    }
    try {
      const highlight = CM.HighlightStyle.define([
        { tag: CM.tags.keyword, class: "tok-keyword" },
        { tag: CM.tags.typeName, class: "tok-type" },
        { tag: CM.tags.function(CM.tags.variableName), class: "tok-fn" },
        { tag: CM.tags.number, class: "tok-num" },
        { tag: [CM.tags.string, CM.tags.character], class: "tok-str" },
        { tag: CM.tags.special(CM.tags.string), class: "tok-interp" },
        { tag: CM.tags.lineComment, class: "tok-comment" },
        { tag: CM.tags.operator, class: "tok-op" },
        { tag: CM.tags.invalid, class: "tok-invalid" },
      ]);

      const view = new CM.EditorView({
        doc: els.src.value,
        extensions: [
          CM.lineNumbers(),
          CM.highlightActiveLine(),
          CM.highlightActiveLineGutter(),
          CM.drawSelection(),
          CM.bracketMatching(),
          CM.history(),
          CM.indentUnit.of("    "),
          CM.keymap.of([...CM.defaultKeymap, ...CM.historyKeymap, CM.indentWithTab]),
          window.originMode(CM),
          CM.syntaxHighlighting(highlight),
          CM.EditorView.lineWrapping,
          CM.EditorView.updateListener.of((u) => { if (u.docChanged) remember(); }),
        ],
      });
      els.src.replaceWith(view.dom);
      view.dom.style.flex = "1";
      view.dom.style.minHeight = "0";
      editor = {
        get: () => view.state.doc.toString(),
        set: (v) => view.dispatch({ changes: { from: 0, to: view.state.doc.length, insert: v } }),
        focus: () => view.focus(),
      };
      els.kind.textContent = "";
    } catch (e) {
      // A broken editor must not take the page with it: the textarea is still in the DOM
      // and still works.
      els.kind.textContent = "plain editor";
    }
  }

  // ---------------------------------------------------------------- running

  let worker = null;
  let running = false;

  function newWorker() {
    const w = new Worker("worker.js");
    w.onmessage = onMessage;
    w.onerror = () => {
      say("the playground could not start its worker", true);
      discardWorker();
      setRunning(false);
    };
    return w;
  }

  // A terminated worker is a discarded module (ADR-0036): a program killed mid-collection
  // leaves state nobody should inherit. A worker that finished normally is reused, which is
  // what keeps the second run from paying the module's instantiation cost again.
  function discardWorker() {
    if (worker) worker.terminate();
    worker = null;
  }

  function setRunning(on) {
    running = on;
    els.run.disabled = on;
    els.stop.disabled = !on;
    els.status.textContent = on ? "running" : "";
  }

  function say(text, bad) {
    els.host.textContent = text;
    els.host.classList.toggle("bad", !!bad);
  }

  function onMessage(e) {
    const m = e.data;
    if (m.kind === "output") {
      // Chunks arrive while the program is still running, so a program that prints and then
      // loops forever still shows what it printed.
      els[m.stream].textContent += m.text;
      return;
    }
    if (m.kind === "failed") {
      say("the playground could not run this: " + m.message, true);
      discardWorker();
      setRunning(false);
      return;
    }
    // `done` carries the full streams; the chunks were the same bytes delivered early.
    els.stdout.textContent = m.stdout;
    els.stderr.textContent = m.stderr;
    let note = "exit status " + m.exit;
    if (m.exit === 0) note = "finished · " + note;
    if (m.truncated) note += " · output stopped at the 4 MiB limit";
    if (m.internalError) note += " · " + m.internalError;
    say(note, m.exit !== 0);
    setRunning(false);
  }

  function runProgram() {
    if (running) return;
    els.stdout.textContent = "";
    els.stderr.textContent = "";
    say("");
    setRunning(true);
    els.status.textContent = "compiling";
    if (!worker) worker = newWorker();
    worker.postMessage({
      source: editor.get(),
      engine: els.engine.value,
      opt: Number(els.opt.value),
      stdin: els.stdin.value,
    });
  }

  function stopProgram() {
    if (!running) return;
    discardWorker();
    setRunning(false);
    // Termination is not a language event (ADR-0036): no trap, no exit status. The page
    // reports that it stopped the program, in its own words.
    say("stopped — the program did not finish, so it has no exit status");
  }

  // ---------------------------------------------------------------- state

  function remember() {
    store.set(STORE.source, editor.get());
  }

  function fillExamples() {
    const list = window.originExamples || [];
    for (const ex of list) {
      const o = document.createElement("option");
      o.value = ex.name;
      o.textContent = ex.title;
      els.example.appendChild(o);
    }
  }

  function exampleByName(name) {
    return (window.originExamples || []).find((e) => e.name === name) || null;
  }

  async function copyLink() {
    const result = await window.originShare.encode(editor.get());
    if (result.tooLong) {
      say(`this program is too long to put in a link (${result.size} characters of ` +
          `${result.limit}). Nothing was copied — a link with half a program in it would ` +
          `be worse than none.`, true);
      return;
    }
    const url = location.origin + location.pathname + "#" + result.fragment;
    try {
      await navigator.clipboard.writeText(url);
      history.replaceState(null, "", "#" + result.fragment);
      say("link copied. It contains the whole program, so anyone with it has a copy.");
    } catch (e) {
      // Clipboard access can be refused. Putting it in the address bar still gives the
      // visitor the link, which is the thing they asked for.
      history.replaceState(null, "", "#" + result.fragment);
      say("the link is in your address bar — copying it needed permission this browser did not give.");
    }
  }

  // ---------------------------------------------------------------- start

  async function start() {
    fillExamples();

    // A shared link wins over remembered work: someone who opened a link came to see what is
    // in it. Their own program is untouched in storage, and this does not overwrite it until
    // they type.
    let initial = null;
    let fromLink = false;
    if (location.hash.length > 2) {
      initial = await window.originShare.decode(location.hash);
      fromLink = initial !== null;
      if (!fromLink) say("that link could not be read, so this is your own program instead.", true);
    }
    if (initial === null) initial = store.get(STORE.source);
    if (initial === null) {
      const first = exampleByName("fib") || (window.originExamples || [])[0];
      initial = first ? first.source : "";
    }
    els.src.value = initial;

    const engine = store.get(STORE.engine);
    if (engine) els.engine.value = engine;
    const opt = store.get(STORE.opt);
    if (opt) els.opt.value = opt;
    const stdin = store.get(STORE.stdin);
    if (stdin !== null) els.stdin.value = stdin;

    upgradeEditor();
    if (fromLink) say("this program came from a shared link.");

    els.run.disabled = false;
    els.run.onclick = runProgram;
    els.stop.onclick = stopProgram;
    els.share.onclick = copyLink;

    els.engine.onchange = () => store.set(STORE.engine, els.engine.value);
    els.opt.onchange = () => store.set(STORE.opt, els.opt.value);
    els.stdin.oninput = () => store.set(STORE.stdin, els.stdin.value);

    els.example.onchange = () => {
      const ex = exampleByName(els.example.value);
      if (!ex) return;
      editor.set(ex.source);
      // An example that reads standard input brings its own, so choosing it from the list
      // gives a program that runs rather than one that reads nothing and looks broken.
      els.stdin.value = ex.stdin || "";
      store.set(STORE.stdin, els.stdin.value);
      remember();
      els.stdout.textContent = "";
      els.stderr.textContent = "";
      say("");
      // A shared link in the address bar would no longer describe what is in the editor.
      history.replaceState(null, "", location.pathname);
      editor.focus();
    };

    els.forget.onclick = () => {
      store.remove(STORE.source);
      store.remove(STORE.engine);
      store.remove(STORE.opt);
      store.remove(STORE.stdin);
      say("forgotten. What is in the editor now is not saved unless you change it.");
    };

    // Ctrl/Cmd+Enter runs, which is what every editor in this category does.
    document.addEventListener("keydown", (e) => {
      if ((e.metaKey || e.ctrlKey) && e.key === "Enter") {
        e.preventDefault();
        runProgram();
      }
    });

    if (!store.set(STORE.source, initial)) {
      $("storage-note").textContent =
        "This browser is not letting the page store anything, so your program will be gone when you reload.";
    }
  }

  start();
})();
