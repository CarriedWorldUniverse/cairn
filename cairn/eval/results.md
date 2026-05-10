# Simplifier Comparison Results

Running log of `compare.sh` runs. Add a section per run with engine
versions, sample, latency, rough cost, and your quality rating. Keep
old runs — they're the historical record showing how model quality
moved over time relative to Cairn's needs.

Quality rubric (subjective, 1-5):
- 5: explains intent + risks better than the diff itself does
- 4: accurate intent, may miss nuance
- 3: technically correct but reads like a paraphrase of the diff
- 2: misses intent or hallucinates context
- 1: unusable

## Template (copy for new runs)

```markdown
## YYYY-MM-DD — <reason for run>

**Operator:** alice
**Cairn commit:** <git rev>
**System prompt:** services/cairn/summarizer/prompt.go (refresh extracted)

| Engine | Version / model id | Sample | Latency (s) | Cost est. | Quality 1-5 | Notes |
|---|---|---|---|---|---|---|
| claude-code | claude-sonnet-4-5 | 001-data-model-fix | | subscription | | |
| deepseek-chat | deepseek-chat | 001-data-model-fix | | ~$0.0001 | | |
| ollama-zaya | zaya1-8b | 001-data-model-fix | | $0 (testing on <server-host>) | | |

**Decision:** keep `claude-code` / switch to `deepseek-chat` / switch to `ollama-zaya` / re-run with different samples / inconclusive.

**Notes:**
- (free-text observations: hallucinations, context-window issues, surprising wins, etc.)
```

---

## Runs

(Append entries below as comparisons are run.)
