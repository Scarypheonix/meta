# ADR-0035: A shared program travels in the URL fragment, compressed, and ships in v1

**Status:** accepted · **Date:** 2026-09-08 · **Decided by:** implementer (user delegated)

## Context

Every playground in this category has a share button, and it is the feature that makes one
useful to anybody but its visitor: a link in a bug report, a link in a chat, a link in a
teaching note. The Go and Rust playgrounds both have it, and both do it with a server —
`play.golang.org` stores a snippet and hands back an ID.

This site has no server and is not getting one. The phase's constraints are explicit that a
backend is out of scope, and that if a feature seems to need one the answer is to stop and
ask rather than to add it. So the question is whether sharing can be done with zero backend,
and if so, where the bytes go.

They can go in three places:

- **The path** — needs a server to resolve it. Out.
- **The query string** (`?code=...`) — sent to the server on every request. On static
  hosting that means the host's access log, and any proxy or CDN in between, receives every
  shared program.
- **The fragment** (`#code=...`) — by construction never transmitted. It is not in the HTTP
  request line, so no server, log, CDN or proxy sees it. Only the browser that opened the
  link.

That difference is not a detail here. The site's claim to a visitor is that their code
stays in their browser, and the fragment is the only one of the three that keeps that claim
true of a *shared* link as well as an unshared one.

## Options considered

1. **Defer sharing to a later phase.** Keeps this phase smaller. Costs the site the one
   feature that makes a playground link worth having, for a saving measured in a few dozen
   lines.
2. **Query string.** Works, and quietly turns the hosting provider's access log into a
   record of every program anyone shared. Contradicts the privacy property the page states
   in its own UI.
3. **Fragment, source encoded verbatim** (percent- or base64-encoded). Simple, no
   compression dependency, and long: base64 inflates by a third before anything else.
4. **Fragment, compressed then base64url-encoded.** `CompressionStream('deflate-raw')` is
   in every current browser and needs no library. Origin source is ordinary text and
   compresses accordingly; the encoding is decoded by `DecompressionStream` on load.

## Decision

**Option 4, in v1.**

A shared program is `deflate-raw`-compressed, base64url-encoded, and placed in the URL
fragment along with a short version marker so the encoding can change later without
breaking links already in the wild.

It ships in v1 rather than being deferred because it costs a few dozen lines given that
there is no backend to build, and because the alternative is a playground whose output
cannot be handed to anyone — which is most of what a playground is for.

Where `CompressionStream` is unavailable, the encoding falls back to base64url of the raw
source under a different version marker. Both directions decode both markers, so a link
made by one browser opens in the other.

**A length ceiling is enforced when a link is made, not when one is opened.** Past a
documented size the page says the program is too large to put in a link and does not
produce one. It never emits a truncated link: a link that silently drops the second half of
a program is the worst outcome available here, because it looks like it worked and
reproduces something that is not what was shared. Opening a link has no ceiling — whatever
decodes, runs.

## Consequences

- **Nothing about a shared program reaches any server**, including this one, including the
  static host. That is a structural property of where the bytes are, not a policy.
- **A link contains the whole program**, so it is long, and length scales with the source.
  This is the honest cost of having no backend: there is no ID to hand out because there is
  nothing storing anything.
- **Links do not expire and cannot be revoked**, because nothing hosts them. A shared link
  is a copy of the program, in the hands of whoever has the link. The UI should say so
  plainly rather than let a visitor assume a share is a private handoff.
- **The version marker is what makes the encoding reversible.** Changing the compression
  later means teaching the decoder one more marker, not breaking every link ever made.
- **Sharing is not persistence.** A visitor's own saved work is a separate mechanism in
  their own browser storage, and the two must not be confused in the UI: one is "a copy for
  someone else", the other is "mine, here, until I clear site data".
- **The fragment is also how examples are addressed.** The example programs from
  `docs/spec/10-examples.md` can be linked with the same mechanism and need no separate
  route, which is one code path instead of two.

## Reversing it

Adding a server-backed short link later is additive: a new marker, resolved by fetch
instead of decoded locally, with fragment links continuing to work untouched. It would
also be the point at which this project acquires a backend, a database and a moderation
problem — the phase brief says to stop and ask before that, and this ADR is not that ask.
