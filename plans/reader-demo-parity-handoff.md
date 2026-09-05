# Reader demo parity: implementation handoff

## 1. Objective and delivery boundary

The owner can read a long Dutch article in the real local Doublangu website,
see an English subtitle beneath every word, understand connected expressions,
and focus a sentence without making any words wrap again or moving neighboring
sentences. The visual frontend and local design runner are authored on branch
`codex/reader-demo-design`; continue from that branch, not from a fresh redesign.
This is a self-contained handoff, not an AFlow plan. Keep this plan read-only;
record implementation progress and verification in DEVLOG.

Deliver locally first. Leave the local application running for owner review,
expect a final course-correction pass, and report unresolved defects accurately.
No production deployment, automatic main merge, public push, or public PR is
authorized by this plan. The public repository auto-deploys every main push;
obtain the owner's explicit publication/deployment approval separately.

## 2. Product decisions already made

1. Follow the original demo: generous serif Dutch, small sans-serif subtitles,
   softly outlined focused sentence, warm expression marks, quiet reader chrome.
   The latest instructions supersede the old demo's reflowing font-size change.
2. Preserve both selectable experiences until the owner tries them: **Enlarge
   without reflow** (rest scale 0.94, focus scale 1) and **Highlight only** (scale
   1 throughout). Layout always reserves the maximum metrics. Do not implement
   focus font-size/letter-spacing changes, scroll compensation, or detached copies.
3. Every word has a subtitle at all times, including articles, pronouns,
   prepositions, learned words, and words within idioms. Ordinary words use their
   contextually appropriate individual meaning. Idiom members retain their own
   literal word gloss in addition to the independent idiomatic meaning.
4. For example, `tot rust te komen` retains `to / rest / to / come` under its
   individual words and separately displays `to calm down`. `gaf … op` retains
   `gave` and `up`, with a dashed connector and separate `gave up` meaning.
   `gooide gisteren bijna het bijltje erbij neer` must exclude `gisteren` and
   `bijna` from the construction's explicit members. Never mark every word in
   the minimum-to-maximum expression span merely because it lies between anchors.
5. A legitimate same-spelling English translation is allowed: Dutch `plan`,
   `bank` (financial institution), or `in` can have the same English spelling.
   Names display their name and numbers their value; do not pretend they are
   untranslated mistakes. A sofa sense of `bank` must remain distinct from a
   financial sense. Learning stays semantic-sense keyed.
6. Contiguous members have a clean bracket. Discontinuous gaps use dashed curves.
   Across wrapped lines, continue at the margins with matching numbered labels;
   never draw a diagonal over intervening source lines. Overlapping constructions
   keep separate connector lanes and matching footer keys.
7. Mobile is a reading surface, not a stack of control panels: compact gutters,
   no repeated sentence labels or unavailable-audio messages, inline expression
   meaning keys, always-visible focus selector, **Aa** for appearance settings.
   Preserve source >=20 rendered pixels and subtitles >=12 at rest. Do not make
   labels tiny, truncate them, or hide them to achieve density.
8. Ink, Paper, Sepia, and Contrast cover the page, chrome, focus surface, and
   popover. Main/subtitle/accent/construction text must remain >=4.5:1 against
   both the page and focus surface. Do not dim surrounding text with opacity.

## 3. Now versus out of scope

**Now:** complete real analysis data for the already-authored reader; preserve
exact membership and per-word meanings through storage/API; exercise real saved
articles locally; verify both focus modes on long content; return to the owner
for final visual review and make a bounded correction pass.

**Out of scope:** new frameworks, new database/service architecture, ingestion
features, a generic fixture system, dictionary guesses in the frontend,
pagination/virtualization without a measured need, new learning identity systems,
provider deployment, worker reenrollment, production data migration, public
publishing, or redesigning unrelated library/settings screens. Do not regenerate
existing production articles or reroute an enrolled Mac worker to the local API.

## 4. Starting state and drift resolution

1. Read root and nearest AGENTS files, then run `hostname`, `pwd`, `git status
   --short`, `git branch --show-current`, `git log -5 --oneline`, and `git
   worktree list`. Preserve every unrelated change. The owner's Mac assets
   `Qwen3.5-2B-Q8_0.gguf` and `voice_nl.flac` are unrelated and remain untracked.
2. Base main is `5b27e39a466ec32450db67c118564e3d398216f8`. p100's former main
   included patch-equivalent commit `2c4ce0b` plus five dirty files. The exact
   dirty state was committed as `5a5b794` on p100 branch
   `codex/p100-drift-preserved-20260905`, fetched to Mac as
   `backup/p100-drift-20260905`. Its source/config fixes were already in main;
   four unique deployment-history sections were recovered into DEVLOG. p100 main
   was rebased onto the same clean origin main. Do not replay duplicate fixes or
   remove these recovery refs.
3. The Mac is explicitly authorized for this local UI iteration. If backend
   implementation runs in the usual p100 checkout, first bring the design branch
   across as committed Git history and verify identical heads. At delivery,
   transfer the implementation commits back to the Mac, rebuild the local API,
   and test there. Never maintain divergent copies or copy dirty source trees
   over one another. Use a Git bundle for a private transfer if public publishing
   is not yet approved. Stop for competing work instead of overwriting it.

## 5. Authored frontend and local preview

1. `web/src/lib/reader/semanticRuns.ts`: individual-word mode preserves lexical
   occurrences and attaches exact construction membership. Legacy callers keep
   the previous grouping default. `Sentence.svelte` and `Paragraph.svelte` use
   individual-word mode. Source is sliced from canonical UTF-16 spans.
2. `TextOccurrence.svelte`: inline-grid source/subtitle, attached punctuation,
   persistent visible subtitle using stored word shadow or word sense fallback,
   exact member highlight. Hover previews the first associated expression; click
   opens the individual word. Other expressions are available through footer keys.
3. `Sentence.svelte`: fixed-layout sentence card, transform-only enlargement,
   reserved footer/connector space, keyboard/touch/focus/hover handling, actual
   sentence-audio callback only when ready. `ConstructionOverlay.svelte` draws
   the measured SVG bracket/gap/continuation marks; no focus-driven measurement.
4. `ArticleReader.svelte`, `ReaderToolbar.svelte`, `theme.ts`, `web/src/app.css`,
   `web/src/routes/+layout.svelte`, and `web/src/routes/reader/[id]/+page.svelte`
   own the page and themes. The previous scroll-compensation call is removed.
   `focusController.ts` remains unused compatibility code, not an implementation path.
5. `web/dev/readerFixture.ts` has the six-sentence construction stress sample.
   `web/dev/longReaderFixture.ts` is an original 871-word story in 12 paragraphs,
   four sentences each, with authored glosses and rebased block offsets. It is
   not repeated filler or a translation-quality benchmark. `readerDemoPlugin.ts`
   serves only these exact synthetic IDs in explicitly enabled Vite development.
6. `tools/local-reader.mjs` starts the real Go service on 127.0.0.1:8097 and Vite
   on 127.0.0.1:5177. It uses isolated data/media/owner credentials under
   `data/reader-design`, strips inherited DOUBLANGU configuration, disables
   analysis, and refuses occupied ports. Sample writes fail explicitly. The
   login gate and preference API remain real; no production database is copied.

Local commands, from the Mac checkout:

```sh
npm --prefix web ci
node tools/local-reader.mjs
```

Open `http://127.0.0.1:5177/reader/01J00000000000000000000LONG`, use the locally
printed password if asked, and use **Short examples** to compare constructions.
Do not put the password, local database, media, or runtime logs in Git. After a
backend rebuild, stop only this launcher's processes and restart this command;
the existing local owner and data persist. A detached tmux session named
`doublangu-reader-design` may already own these ports; inspect it before starting
another instance. Do not kill unrelated processes to free a port.

## 6. Implementation sequence for the receiving agent

### 6.1 Preserve individual meanings through analysis

Read `internal/pipeline/AGENTS.md`, then inspect:

```sh
rg -n 'shadow_text|unchanged|proper_name|primary_translation|PromptVersion' internal/annotator/stages.go internal/annotator/schema.go internal/semantics/stages.go internal/semantics/types.go internal/pipeline/types.go
rg -n 'contiguous_group_member|show_shadow|finishOccurrenceDisplay|effectiveShadow' internal/reader
rg -n 'same|equal|copy|unchanged|subtitle' internal/semantics/*test.go internal/annotator/*test.go
```

1. Update the existing linguistic and translation prompts, including the
   compatibility analysis prompt discovered by `rg -n 'shadow_text' internal/annotator`.
   Require individual lexical meanings for function words and idiom members;
   keep construction translation separate and preserve exact deterministic IDs.
   Use existing fields and stages. Do not add a new translation service/schema
   merely to carry information already supported by word and construction senses.
2. Review `validateTranslatedSubtitle` in `internal/semantics/stages.go` and the
   corresponding merged validation in `internal/semantics/types.go`. Replace the
   blanket source-equals-subtitle rejection with acceptance of legitimate
   same-spelling English senses, while retaining nonempty lexical coverage,
   reference validity, exact UTF-16/source checks, bounded safe strings, and
   construction validation. Align special-name/number/acronym handling with
   visible identity labels. Do not allow arbitrary `unchanged` to bypass missing
   glosses for ordinary words. Add positive and negative test fixtures first.
3. Update the existing prompt/cache identity constants in `internal/pipeline/types.go`
   and the compatibility prompt constant in `internal/semantics/types.go` so old
   cached artifacts cannot silently satisfy the new meaning requirements. Keep
   contract shape/version unchanged if the shape is unchanged. Use the existing
   exact-cache/fresh-run mechanisms; do not add a migration or cache subsystem.
4. Regression cases must include every short-sample sentence, legitimate `plan`
   and financial `bank`, sofa `bank`, `in`, names and numbers, idiom literal
   glosses, two constructions in one sentence, inserted modifiers, and long
   block-relative UTF-16 offsets. Test invalid missing words and invalid member
   references as rejections. Do not evaluate quality only on the mocked fixture.

### 6.2 Materialize and expose complete word subtitles

1. Inspect `internal/reader/chunk_publish.go` and `internal/reader/v2_store.go`,
   including `finishOccurrenceDisplay`. Retain each word's authored subtitle in
   storage/API even if it belongs to a contiguous group. Align `show_shadow` and
   suppression metadata with the new persistent-visible policy rather than
   relying solely on the frontend to ignore outdated flags.
2. A learned sense remains learned but its subtitle stays visible. Keep rollback
   on failed learning saves and semantic-sense identity. Do not mark all spellings
   of a homonym learned together. Construction learning and lexical learning are
   distinct existing identities; do not create a new state system.
3. Keep exact `member_occurrence_ids` through chunk publication and retrieval.
   Do not turn sparse construction spans into a blanket member range. Add store
   round-trip and HTTP article-response tests proving both lexical subtitles and
   expression meaning survive publication and rereading.
4. Existing saved articles can lack individual glosses entirely. Preserve them
   and use an explicit **Fresh analysis** on local test articles to repair data.
   Do not manufacture missing translations or bulk-reanalyze production. Keep
   missing-data feedback honest during analysis/failure; `·` is only the current
   visual placeholder, not completion of the always-visible-subtitle requirement.
5. Inspect the no-sentence compatibility path in `Paragraph.svelte`. It still
   renders legacy runs without sentence connectors. Prefer ensuring current
   saved articles expose deterministic sentences through the existing server
   path; avoid building a second frontend sentence parser or rewriting legacy
   storage. Verify current real saved articles take the sentence path.

### 6.3 Deliver the real-data path to the local application

1. Keep both development samples working. They let the owner compare layout
   without consuming provider quota or waiting for analysis. Do not replace them
   with a second mock-only frontend.
2. Use the existing local article-creation route to save the long fixture's
   canonical paragraphs in the isolated local database. For deterministic tests,
   reuse the repository's existing annotator/provider test seams to publish
   analysis through the real pipeline/store, not a browser-only fabricated API.
   Add the smallest Go integration fixture needed; do not create a generic seed
   framework or directly copy production SQLite files.
3. For an actual provider smoke, use a locally available authenticated provider
   selected through the existing configuration path. Never inherit remote
   secrets or enroll/reroute the existing production Mac speech worker. If no
   safe local provider is available, report that exact blocker; complete the
   deterministic store/API path and leave live provider validation explicit.
   Keep any runner option needed for this explicit and default-off; do not change
   the design launcher's safe analysis-disabled default.
4. Confirm real GET article responses contain nonempty individual glosses and
   correct member IDs before judging the frontend. Then exercise the saved
   article on port 5177 in the Codex browser: reload, focus/tap, explore both word
   and expression meanings, change theme, mark a sense learned, and verify no
   reflow or subtitle loss. If audio is unavailable, say so; do not fake playback.
5. Commit the coherent implementation and transfer the exact commits to the Mac
   before running the local build. Verify matching Git heads on both hosts and
   preserve unrelated files. Public publication remains a separate approval.

## 7. Verification and acceptance

On p100 prepend the repository's Go toolchain directory to PATH:

```sh
export PATH=/root/go/pkg/mod/golang.org/toolchain@v0.0.1-go1.26.5.linux-amd64/bin:$PATH
```

Run from the repository root. Do not run Svelte generation/build tasks at the
same time as browser E2E in this checkout; they share generated files.

```sh
go test ./internal/annotator ./internal/semantics ./internal/reader ./internal/httpapi
go test -race ./internal/semantics ./internal/reader ./internal/httpapi
npm --prefix web run validate:openapi
npm --prefix web run generate:api
git diff --exit-code -- web/src/lib/api/generated.ts
npm --prefix web run check
npm --prefix web run test:unit
npm --prefix web run build
npm --prefix web run test:e2e -- reader-design.spec.ts reader.spec.ts reader-progressive.spec.ts reader-preference.spec.ts
make verify
DOUBLANGU_TEST_CODEX_LIVE=1 go test ./internal/annotator -run '^TestLiveCodexAppServer$' -count=1 -v
git diff --check
```

If contract changes prove necessary, change OpenAPI then regenerate output;
never hand-edit generated types. Record exact commands/results, and distinguish
reproduced baseline failures from introduced failures. Do not fix unrelated Go
plugin architecture as part of a reader change.

At this handoff, the native Mac `make verify` reaches the plugin integration
matrix and fails `TestIntegration_FullMatrix` with `host/plugin module graph
differs`. The same failure was reproduced in a clean **ordinary clone** of base
main `5b27e39` using the exact fingerprint command from Makefile. A detached Git
worktree passes, so a worktree-only baseline misses this existing checkout-
sensitive defect. `GOFLAGS=-buildvcs=false` does not remove it. Focused reader Go
tests/race tests, Svelte checks, all 130 unit tests, production build, all 20
reader browser tests, API generation, and the authenticated Codex smoke pass.
Report this existing native-plugin issue separately, not as a reader acceptance
pass or a reason to expand this handoff into plugin work.

Required observable results:

1. Short sample: 66 lexical words with visible subtitles and exact source.
   Long sample: 871 words, 12 paragraphs, 48 sentences; no missing labels,
   overlapping subtitle boxes, or horizontal page overflow at 320/375/1360px.
2. At 375px, the long article body starts before 370px from the page top; it
   contains >60 words per 1,000 vertical pixels, source >=20px and subtitle >=12px
   at the resting scale. Preserve these checks when tuning density.
3. In both focus modes, every lexical offset/size, every card top/height, and
   `scrollY` stay unchanged after focus; repeat at the middle and end of the
   article. Responsive resizing may wrap; focus may not. Include keyboard and
   mobile taps, and check the popup does not hide the user's chosen word.
4. In the real saved-article response, all lexical glosses—including idiom
   members and learned senses—survive a reload and a store round trip. Function
   words have actual meanings, not fabricated generic placeholders.
5. Every construction's connector lands only on its explicit members. Two
   overlapping or separated groups remain distinguishable through their matching
   numbered keys. A wrapped construction uses margin continuations, not a line
   through unrelated text.
6. All four theme contrast checks pass on page and focused surfaces. Existing
   authentication, progressive analysis, preference saves, audio when ready,
   and learning rollback tests still pass. Synthetic endpoints are unavailable
   in a production build, and synthetic prose is absent from browser bundles.

## 8. Final owner review and bounded corrections

1. Leave the local app running and open the long article in the Codex browser.
   Reset temporary viewport overrides. Give the owner the link and access-file
   location, not a password in committed documentation.
2. Ask the owner to read several paragraphs in each focus mode, switch Paper /
   Ink / Sepia, and tap a split expression near the end at phone width. Treat
   their choice of focus feel and mobile density as a final design decision,
   not as evidence already supplied by automated tests.
3. Make a focused correction pass for observed spacing, contrast, connector,
   or focus problems, rerun the relevant checks, and return the exact updated
   local URL/commit. Do not silently replace the demo's design with another UI.
4. Report completed frontend/data-path work, verification, remaining manual
   checks, synchronized commit heads, and the explicit publication/deployment
   gate. Never claim production parity based solely on mocked article responses.
