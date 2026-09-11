// The tutorial page's two buttons per step.
//
// The page shows pictures, and a picture cannot be copied or run. The programs behind these
// buttons come from tutorial-programs.js, which is generated from the same sources the
// pictures are rendered from (tests/web/picture_test.go), so what is copied is always what
// is shown.
//
// "Run it" opens the playground with the program in the link's fragment -- the same
// mechanism the playground's own Copy link button uses (ADR-0035), which browsers never
// send to a server.

(function () {
  "use strict";

  const programs = window.originTutorial || {};

  function button(label, title) {
    const b = document.createElement("button");
    b.textContent = label;
    b.className = "link";
    if (title) b.title = title;
    return b;
  }

  for (const step of document.querySelectorAll(".step[data-program]")) {
    const name = step.getAttribute("data-program");
    const source = programs[name];
    if (typeof source !== "string") continue;

    const row = document.createElement("p");
    row.className = "step-actions";

    const run = button("Run it", "open the playground with this program");
    run.onclick = async () => {
      const result = await window.originShare.encode(source);
      if (result.tooLong) {
        note(row, "that program is too long for a link");
        return;
      }
      window.open("index.html#" + result.fragment, "_blank", "noopener");
    };

    const copy = button("Copy", "copy this program to the clipboard");
    copy.onclick = async () => {
      try {
        await navigator.clipboard.writeText(source);
        note(row, "copied");
      } catch (e) {
        // Clipboard access can be refused, and a button that silently does nothing is
        // worse than one that says so.
        note(row, "this browser did not allow copying");
      }
    };

    row.appendChild(run);
    row.appendChild(copy);
    step.appendChild(row);
  }

  function note(row, text) {
    let el = row.querySelector(".step-note");
    if (!el) {
      el = document.createElement("span");
      el.className = "step-note muted";
      row.appendChild(el);
    }
    el.textContent = text;
  }
})();
