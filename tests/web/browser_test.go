package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/scarypheonix/meta/internal/testutil"
)

// The playground, driven in a real browser.
//
// tests/wasm already holds the module to the corpus, one engine boundary at a time, under
// Node. What it cannot check is the page: that the editor loads, that the examples on the
// menu are the programs they claim to be, that output reaches the DOM, and that a shared
// link round-trips. Those need a browser, and this is the only test in the project that
// wants one.
//
// It skips rather than failing when the browser is not installed (process rule 8: a test
// that cannot run says so with the reason named, and never passes silently), which is the
// arrangement tests/debuginfo has for lldb.

// driver is the page-driving half, in the browser's own language. It returns JSON on stdout
// and Go decides whether the answers are right -- the same split tests/wasm uses, and for
// the same reason: the oracle is the corpus, and the corpus is on the Go side.
const driver = `
const { chromium } = require('playwright');
const base = process.argv[2];
const plan = JSON.parse(require('fs').readFileSync(process.argv[3], 'utf8'));

(async () => {
  const browser = await chromium.launch();
  const page = await browser.newPage();
  const pageErrors = [];
  page.on('pageerror', e => pageErrors.push(String(e.message)));
  page.on('console', m => { if (m.type() === 'error') pageErrors.push(m.text()); });

  await page.goto(base + '/index.html');
  await page.waitForFunction(() => !document.getElementById('run').disabled, null, { timeout: 60000 });

  const out = { pageErrors, runs: [], editor: {}, share: {} };

  out.editor.codemirror = await page.evaluate(() => !!document.querySelector('.cm-editor'));
  out.editor.examples = await page.evaluate(() =>
    Array.from(document.getElementById('example').options).map(o => o.value).filter(v => v));
  // The mode is doing something: the default program is fib, which has keywords and a type.
  out.editor.keywordTokens = await page.evaluate(() => document.querySelectorAll('.tok-keyword').length);
  out.editor.typeTokens = await page.evaluate(() => document.querySelectorAll('.tok-type').length);

  for (const c of plan.cases) {
    await page.selectOption('#example', c.name);
    await page.selectOption('#engine', c.engine);
    await page.click('#run');
    await page.waitForFunction(
      () => document.getElementById('host').textContent.includes('exit status'),
      null, { timeout: 120000 });
    out.runs.push({
      name: c.name,
      engine: c.engine,
      stdout: await page.textContent('#stdout'),
      stderr: await page.textContent('#stderr'),
      host: await page.textContent('#host'),
    });
  }

  // A shared link must survive the round trip through the fragment, in the page itself
  // rather than in a unit test of the codec: what matters is that opening the link puts the
  // program back in the editor.
  const shared = plan.share;
  const fragment = await page.evaluate(async (src) => {
    const r = await window.originShare.encode(src);
    return r.fragment || null;
  }, shared);
  out.share.fragment = fragment;
  if (fragment) {
    await page.goto(base + '/index.html#' + fragment);
    await page.waitForFunction(() => !document.getElementById('run').disabled, null, { timeout: 60000 });
    out.share.recovered = await page.evaluate(() => {
      const cm = document.querySelector('.cm-editor');
      if (cm) return cm.cmView ? null : window.getSelection && document.querySelector('.cm-content').textContent;
      return document.getElementById('src').value;
    });
  }

  // Everything above this line is the page behaving normally, so what it reported is what
  // the test judges. The fallback phase below blocks a request on purpose, and the failed
  // load it produces is the point of the exercise rather than a defect.
  out.pageErrors = pageErrors.slice();

  // The editor must not be on the critical path (ADR-0034): with the bundle unavailable the
  // page still runs Origin, in the textarea it shipped with.
  await page.route('**/codemirror.js', route => route.abort());
  await page.goto(base + '/index.html');
  await page.waitForFunction(() => !document.getElementById('run').disabled, null, { timeout: 60000 });
  out.fallback = {
    codemirror: await page.evaluate(() => !!document.querySelector('.cm-editor')),
    kind: await page.textContent('#editor-kind'),
  };
  await page.evaluate(() => { document.getElementById('src').value = 'use std::io;\nfn main() { io::println("fallback"); }\n'; });
  await page.click('#run');
  await page.waitForFunction(
    () => document.getElementById('host').textContent.includes('exit status'),
    null, { timeout: 120000 });
  out.fallback.stdout = await page.textContent('#stdout');

  console.log(JSON.stringify(out));
  await browser.close();
})().catch(e => { console.error('DRIVER FAILED: ' + (e && e.stack || e)); process.exit(1); });
`

type browserResult struct {
	PageErrors []string `json:"pageErrors"`
	Editor     struct {
		CodeMirror    bool     `json:"codemirror"`
		Examples      []string `json:"examples"`
		KeywordTokens int      `json:"keywordTokens"`
		TypeTokens    int      `json:"typeTokens"`
	} `json:"editor"`
	Runs []struct {
		Name   string `json:"name"`
		Engine string `json:"engine"`
		Stdout string `json:"stdout"`
		Stderr string `json:"stderr"`
		Host   string `json:"host"`
	} `json:"runs"`
	Share struct {
		Fragment  string `json:"fragment"`
		Recovered string `json:"recovered"`
	} `json:"share"`
	Fallback struct {
		CodeMirror bool   `json:"codemirror"`
		Kind       string `json:"kind"`
		Stdout     string `json:"stdout"`
	} `json:"fallback"`
}

func TestThePlaygroundRunsItsExamplesInABrowser(t *testing.T) {
	root := testutil.RepoRoot(t)
	node, nodePath := requireBrowser(t)

	dir := stageSite(t, root)
	srv := httptest.NewServer(http.FileServer(http.Dir(dir)))
	defer srv.Close()

	// Every example on both engines: the page's engine control is a claim about what runs,
	// and a claim nothing checks is one that quietly stops being true.
	type planCase struct {
		Name   string `json:"name"`
		Engine string `json:"engine"`
	}
	var cases []planCase
	for _, e := range examples {
		for _, engine := range []string{"vm", "interp"} {
			cases = append(cases, planCase{e.name, engine})
		}
	}
	shareSource := readCase(t, root, "closure_counter", ".origin")
	plan, err := json.Marshal(map[string]any{"cases": cases, "share": shareSource})
	if err != nil {
		t.Fatal(err)
	}

	work := t.TempDir()
	writeFile(t, filepath.Join(work, "driver.js"), driver)
	writeFile(t, filepath.Join(work, "plan.json"), string(plan))

	cmd := exec.Command(node, filepath.Join(work, "driver.js"), srv.URL, filepath.Join(work, "plan.json"))
	cmd.Env = append(os.Environ(), "NODE_PATH="+nodePath)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	raw, err := cmd.Output()
	if err != nil {
		t.Fatalf("driving the browser: %v\n%s", err, stderr.String())
	}

	var got browserResult
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("parsing the driver's output: %v\n%s", err, string(raw))
	}

	for _, e := range got.PageErrors {
		t.Errorf("the page reported an error: %s", e)
	}

	// The editor
	if !got.Editor.CodeMirror {
		t.Error("the editor did not load; the page fell back to the plain textarea")
	}
	if got.Editor.KeywordTokens == 0 || got.Editor.TypeTokens == 0 {
		t.Errorf("the Origin mode highlighted nothing: %d keyword and %d type tokens",
			got.Editor.KeywordTokens, got.Editor.TypeTokens)
	}
	if len(got.Editor.Examples) != len(examples) {
		t.Errorf("the menu offers %d examples, the generated list has %d",
			len(got.Editor.Examples), len(examples))
	}

	// The examples, against the corpus's own expected output.
	if len(got.Runs) != len(cases) {
		t.Fatalf("ran %d of %d planned runs", len(got.Runs), len(cases))
	}
	for _, r := range got.Runs {
		want := expectationFor(t, root, r.Name)
		// A trap names the file it happened in, and in the playground that file is called
		// `playground.origin` -- there is no path, because the program came from a text box
		// rather than from disk (cmd/originwasm's defaultName). That is the only thing about
		// these runs that differs from the native corpus, and rewriting the expectation here
		// is the whole of accounting for it: the line, the column, the message and the exit
		// status are all still compared exactly.
		want.stderr = strings.ReplaceAll(want.stderr,
			"tests/e2e/cases/"+r.Name+".origin", "playground.origin")
		if r.Stdout != want.stdout {
			t.Errorf("%s on the %s: stdout differs from tests/e2e.\n got: %q\nwant: %q",
				r.Name, r.Engine, r.Stdout, want.stdout)
		}
		if r.Stderr != want.stderr {
			t.Errorf("%s on the %s: stderr differs from tests/e2e.\n got: %q\nwant: %q",
				r.Name, r.Engine, r.Stderr, want.stderr)
		}
		// The page reports the status in its own words (spec §7), so this checks the number
		// it names rather than the whole sentence.
		if !strings.Contains(r.Host, "exit status "+strconv.Itoa(want.exit)) {
			t.Errorf("%s on the %s: the page reported %q, want exit status %d",
				r.Name, r.Engine, r.Host, want.exit)
		}
	}

	// Sharing
	if got.Share.Fragment == "" {
		t.Error("encoding a program for a link produced nothing")
	}
	if strings.TrimSpace(got.Share.Recovered) == "" {
		t.Error("opening a shared link put no program in the editor")
	} else if !strings.Contains(got.Share.Recovered, "make_counter") {
		t.Errorf("a shared link did not round-trip; the editor holds %q", got.Share.Recovered)
	}

	// The fallback (ADR-0034)
	if got.Fallback.CodeMirror {
		t.Error("CodeMirror was blocked but the page still reported an upgraded editor")
	}
	if got.Fallback.Kind == "" {
		t.Error("the page fell back to the plain editor without saying so")
	}
	if got.Fallback.Stdout != "fallback\n" {
		t.Errorf("with no editor bundle the page could not run a program: stdout = %q",
			got.Fallback.Stdout)
	}
}

// stageSite copies web/ into a temporary directory and builds the module into it, so the
// test serves a complete site without writing into the repository.
func stageSite(t *testing.T, root string) string {
	t.Helper()
	dir := t.TempDir()
	src := filepath.Join(root, "web")
	entries, err := os.ReadDir(src)
	if err != nil {
		t.Fatalf("reading web/: %v", err)
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		b, err := os.ReadFile(filepath.Join(src, e.Name()))
		if err != nil {
			t.Fatalf("reading web/%s: %v", e.Name(), err)
		}
		writeFile(t, filepath.Join(dir, e.Name()), string(b))
	}

	cmd := exec.Command("go", "build", "-o", filepath.Join(dir, "origin.wasm"), "./cmd/originwasm")
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "GOOS=js", "GOARCH=wasm")
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("building the browser host:\n%s", b)
	}

	// wasm_exec.js is committed, and web/README.md pins it to the toolchain that builds the
	// module. If the two ever drift the page fails in a way that is hard to read, so the
	// staged copy is the one the current toolchain ships.
	goroot, err := exec.Command("go", "env", "GOROOT").Output()
	if err != nil {
		t.Fatalf("finding GOROOT: %v", err)
	}
	shim := filepath.Join(strings.TrimSpace(string(goroot)), "lib", "wasm", "wasm_exec.js")
	b, err := os.ReadFile(shim)
	if err != nil {
		t.Fatalf("reading %s: %v", shim, err)
	}
	committed, err := os.ReadFile(filepath.Join(src, "wasm_exec.js"))
	if err != nil {
		t.Fatalf("reading web/wasm_exec.js: %v", err)
	}
	if string(b) != string(committed) {
		t.Errorf("web/wasm_exec.js differs from this toolchain's %s.\n"+
			"They change together with the module's ABI; copy it again:\n  cp %s web/wasm_exec.js",
			shim, shim)
	}
	return dir
}

// requireBrowser finds node and the directory playwright is installed in.
//
// Playwright is not a dependency of this repository -- there is no package.json and this is
// the only test that wants a browser -- so it is looked for where npm puts global packages
// and the test skips when it is not there.
func requireBrowser(t *testing.T) (node, nodePath string) {
	t.Helper()
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed; it is what drives the browser")
	}
	out, err := exec.Command("npm", "root", "-g").Output()
	if err != nil {
		t.Skip("`npm root -g` failed, so playwright cannot be located")
	}
	nodePath = strings.TrimSpace(string(out))
	if _, err := os.Stat(filepath.Join(nodePath, "playwright")); err != nil {
		t.Skipf("playwright is not installed in %s; install it to run the browser test", nodePath)
	}
	return node, nodePath
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}
}
