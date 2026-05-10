# Cairn Simplifier Model Evaluation

Apples-to-apples comparison of summarization engines against Cairn's actual
simplifier prompt and a corpus of real PRs. Provides ongoing record so we
can re-run as new models ship and decide whether to swap the
production simplifier provider.

## What this answers

> "Should we keep `claude-code` as the simplifier backend, or switch to
> something cheaper / sovereign / faster / smarter?"

The answer changes over time as:
- New open-weight models ship (Llama → Qwen → DeepSeek → ZAYA → ...)
- Anthropic / DeepSeek / etc. change pricing
- Cairn's deployment ceiling shifts (personal-substrate → multi-tenant)
- Bridle gains/loses provider support

This kit lets you re-run the comparison without redoing the prompt-extraction
and harness work each time.

## Structure

```
cairn/eval/
├── README.md             — this file
├── system-prompt.txt     — Cairn's literal SystemPrompt, extracted
├── extract-prompt.sh     — refresh system-prompt.txt from prompt.go
├── compare.sh            — 3-way comparison harness
├── results.md            — running log of comparison runs (committed)
├── samples/              — corpus of PR inputs (committed)
│   └── *.md              — one file per representative PR shape
└── runs/                 — raw per-run outputs (gitignored)
    └── <run-id>/         — stamped with date + engine
```

## Running a comparison

```bash
cd cairn/eval

# Refresh the prompt from source (in case it changed):
./extract-prompt.sh

# Configure engines via env (edit if needed):
export DEEPSEEK_API_KEY='<get from platform.deepseek.com>'
export OLLAMA_URL='http://<dmon-tailnet-ip>:11434'
export OLLAMA_MODEL='zaya1-8b'

# Run against a sample (or all of them in a loop):
./compare.sh samples/001-data-model-fix.md

# Skip an engine you don't want to test:
SKIP_DEEPSEEK=1 ./compare.sh samples/004-doc-only.md
```

Results land in `runs/<basename>-<timestamp>-<engine>.txt`.

## Adding to `results.md`

After running a comparison, append a row to `results.md` with date, engine
versions, latency, cost estimate, and your subjective quality rating.
That's the ongoing record — the raw outputs in `runs/` are useful for
the immediate comparison but not worth committing en masse.

If a comparison run produces results that meaningfully change the
production-provider decision (e.g., DeepSeek beats Claude on quality),
also update `docs/cairn/specs/2026-05-10-cairn-ai-native-amendment.md`
§3.3 to reflect the new recommendation.

## Adding a new engine

Edit `compare.sh` to add a new `run_<engine>` function following the
`run_openai_compat` pattern. If the engine is OpenAI-API-compatible
(most are), set `OPENAI_BASE_URL` and `OPENAI_API_KEY` as overrides.

## Adding a new sample PR

The corpus lives in `samples/`. Aim for diverse PR shapes:
- Trivial (typo, version bump, comment fix) — model should say "trivial" and stop
- Single-file fix with explanation — model should explain the why
- Multi-file refactor — model should identify the *one* moving part across files
- Doc-only PR — model should distinguish doc from code
- Large diff (near-context-limit) — stress-test for context-window bites

Naming: `NNN-short-slug.md`, sequenced. Sourced from real cairn-repo
history (sanitized if needed).

## What "good output" looks like

The Cairn simplifier exists so a human reviewer can decide whether to
drill into a PR without reading the diff. A good summary:
- Names the *intent* of the PR in one sentence
- Calls out the major moving parts in 2-4 bullets
- Flags risks/tradeoffs the diff itself doesn't make obvious
- Stays under 200 words
- Says "trivial" and stops when appropriate

A bad summary:
- Paraphrases the diff line-by-line ("removed the X, added Y")
- Omits the "why" the commit message explains
- Hallucinates context not in the diff
- Adds caveats about its own limitations
