# Reader interaction invariants

- Use the real reader components for design iteration; fixtures live in
  `web/dev`, never in production component logic or a second mock renderer.
- Learning mode is the default. Every lexical token keeps a visible subtitle,
  including construction members and learned senses. A construction owns an
  additional meaning, not the words' display slots. Never derive missing
  translations from a browser dictionary.
- Condensed mode intentionally hides subtitles, construction overlays, and
  per-sentence footers, and disables focus scaling: sentences flow inline as
  plain paragraphs at 1.65 line-height. Word hover/click and the single
  fixed-height action row stay available. Learning-mode geometry rules below
  do not apply to Condensed.
- Focus must not change layout metrics, wrapping, card heights, or scroll.
  In Learning mode, reserve full-size geometry and connector/footer space
  before interaction; change transform/color only. Keep highlight-only
  available for comparison.
- SVG connectors use untransformed offsets and exact membership IDs. Do not
  measure transformed bounding boxes or connect across intervening source lines.
- Keep punctuation with its preceding word. Preserve canonical source and
  UTF-16 anchors. Initial responsive wrapping is allowed; focus rewrapping is not.
- Verify `reader-design.spec.ts` on the 66-word and 871-word fixtures, including
  320/375px widths and middle/end focus switches, before accepting layout edits.
  Verify `reader-condensed.spec.ts` for mode persistence, plain-paragraph
  source fidelity, focus stability at 320/375/1280px, and the action row.
- Reading mode persists per browser in
  `doublangu.reader.readingMode.v1`; invalid or blocked storage falls back to
  learning. The toggle lives in ReaderToolbar; Condensed replaces per-sentence
  footers with one fixed-height action row (Play + Translate, disabled without
  an active sentence). Only that row's Translate may start generation, with the
  same 350ms dwell / immediate-tap rules as the Learning footer control.
- Run browser E2E separately from Svelte sync/build tasks. Those tasks share
  generated output with the test dev server and can force reloads mid-test.
- Sentence translation starts only from the Translate control next to Play
  sentence: 350ms dwell for hover/focus, immediate for touch/click. The
  popover polls while pending, keeps the saved text during replacement,
  retries only through explicit buttons, and links work to its analysis run.
  Opening another sentence closes the current popover; closing returns focus
  without reopening.
- Explore Regenerate posts regenerate:true, keeps the saved entry readable
  with inline progress, and rejoins a pending generation instead of
  reposting. A failed replacement keeps the old entry with a retry and run
  link.
