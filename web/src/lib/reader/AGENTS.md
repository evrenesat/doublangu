# Reader interaction invariants

- Use the real reader components for design iteration; fixtures live in
  `web/dev`, never in production component logic or a second mock renderer.
- Every lexical token keeps a visible subtitle, including construction members
  and learned senses. A construction owns an additional meaning, not the words'
  display slots. Never derive missing translations from a browser dictionary.
- Focus must not change layout metrics, wrapping, card heights, or scroll.
  Reserve full-size geometry and connector/footer space before interaction;
  change transform/color only. Keep highlight-only available for comparison.
- SVG connectors use untransformed offsets and exact membership IDs. Do not
  measure transformed bounding boxes or connect across intervening source lines.
- Keep punctuation with its preceding word. Preserve canonical source and
  UTF-16 anchors. Initial responsive wrapping is allowed; focus rewrapping is not.
- Verify `reader-design.spec.ts` on the 66-word and 871-word fixtures, including
  320/375px widths and middle/end focus switches, before accepting layout edits.
- Run browser E2E separately from Svelte sync/build tasks. Those tasks share
  generated output with the test dev server and can force reloads mid-test.
