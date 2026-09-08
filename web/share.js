// Sharing a program, with no server involved (ADR-0035).
//
// The program travels in the URL **fragment**, which is the only one of the three places it
// could go that a browser never transmits: it is not in the request line, so no server, log,
// CDN or proxy sees it -- including the static host serving this page. That is what lets the
// UI say a visitor's code stays in their browser and have it remain true of a shared link.
//
// A link is a copy of the program in the hands of whoever holds the link. Nothing hosts it,
// so nothing can expire or revoke it, and the UI says so rather than letting a visitor read
// "share" as "private handoff".

(function () {
  "use strict";

  // The marker says how the rest was encoded, so the encoding can change later without
  // breaking links already made. Both directions understand both markers.
  const DEFLATE = "1"; // deflate-raw, then base64url
  const PLAIN = "0"; // base64url of the source, for a browser with no CompressionStream

  // maxFragment is the ceiling on the encoded payload, checked when a link is MADE.
  //
  // The largest program in the project's own corpus is 7,779 bytes and compresses well
  // under this; the limit exists for someone pasting a whole module in. Browsers accept far
  // longer fragments, but a URL that survives being pasted into a chat, an issue or a mail
  // client is a different and smaller number, and a link that arrives cut in half is worse
  // than no link at all.
  const maxFragment = 16 * 1024;

  function toBase64Url(bytes) {
    let s = "";
    for (let i = 0; i < bytes.length; i++) s += String.fromCharCode(bytes[i]);
    return btoa(s).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
  }

  function fromBase64Url(text) {
    const padded = text.replace(/-/g, "+").replace(/_/g, "/");
    const raw = atob(padded + "=".repeat((4 - (padded.length % 4)) % 4));
    const bytes = new Uint8Array(raw.length);
    for (let i = 0; i < raw.length; i++) bytes[i] = raw.charCodeAt(i);
    return bytes;
  }

  async function through(stream, bytes) {
    const s = new stream("deflate-raw");
    const writer = s.writable.getWriter();
    writer.write(bytes);
    writer.close();
    const chunks = [];
    const reader = s.readable.getReader();
    for (;;) {
      const { done, value } = await reader.read();
      if (done) break;
      chunks.push(value);
    }
    let total = 0;
    for (const c of chunks) total += c.length;
    const out = new Uint8Array(total);
    let at = 0;
    for (const c of chunks) {
      out.set(c, at);
      at += c.length;
    }
    return out;
  }

  // encode returns {fragment} or {tooLong, size, limit}. It never returns a truncated
  // payload: a link that silently drops the second half of a program looks like it worked
  // and reproduces something that is not what was shared, which is the worst outcome
  // available here.
  async function encode(source) {
    const bytes = new TextEncoder().encode(source);
    let payload;
    if (typeof CompressionStream === "function") {
      payload = DEFLATE + toBase64Url(await through(CompressionStream, bytes));
    } else {
      payload = PLAIN + toBase64Url(bytes);
    }
    if (payload.length > maxFragment) {
      return { tooLong: true, size: payload.length, limit: maxFragment };
    }
    return { fragment: payload };
  }

  // decode returns the source, or null if the fragment is absent or does not decode.
  //
  // There is no ceiling on the way in: whatever decodes, runs. The limit above is about
  // making a link that survives being pasted, and a link that already exists has survived.
  async function decode(fragment) {
    if (!fragment) return null;
    const text = fragment.replace(/^#/, "");
    if (text.length < 2) return null;
    const marker = text[0];
    const body = text.slice(1);
    try {
      if (marker === PLAIN) {
        return new TextDecoder().decode(fromBase64Url(body));
      }
      if (marker === DEFLATE) {
        if (typeof DecompressionStream !== "function") return null;
        return new TextDecoder().decode(await through(DecompressionStream, fromBase64Url(body)));
      }
    } catch (e) {
      return null;
    }
    return null;
  }

  window.originShare = { encode, decode, maxFragment };
})();
