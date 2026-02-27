# Quality Testing Playbook

## Purpose
This guide defines how to run the quality suite and how to make change decisions using the loop:
`implementation -> verification -> keep or rollback`.

## Preconditions
- Run from repository root (`asktg` project directory).
- Local DB with indexed data must exist (default: `%LOCALAPPDATA%\\asktg\\app.db`).
- Embeddings must be configured and vector index must be non-empty.
- Translator settings are read from DB settings and secrets (or fallback env vars).

## Quality suite commands
- Main cross-language quality:
`go test -tags quality -count=1 -run 'TestSearchQualityCrossLanguageComparison$' -v`
- Holdout quality:
`go test -tags quality -count=1 -run 'TestSearchQualityCrossLanguageHoldout$' -v`
- Diagnostics (pipeline attribution):
`go test -tags quality -count=1 -run 'TestSearchDiagnosticsEnglishVsTranslated$' -v`
- Snapshot of indexed corpus:
`go test -tags quality -count=1 -run 'TestSearchQualityIndexSnapshot$' -v`
- Full quality run:
`go test -tags quality -count=1 -run 'TestSearchQualityCrossLanguageComparison$|TestSearchQualityCrossLanguageHoldout$|TestSearchDiagnosticsEnglishVsTranslated$|TestSearchQualityIndexSnapshot$' -v`

## Where to read metrics
- `TestSearchQualityCrossLanguageComparison`:
  - EN: `hit@5`, `hit@10`, `mrr`, `ndcg@20`
  - RU: `hit@5`, `hit@10`, `mrr`, `ndcg@20`
  - RU/EN ratio: `hit@10`, `mrr`, `ndcg`
- `TestSearchQualityCrossLanguageHoldout`:
  - Same metrics on holdout dataset.
- `TestSearchDiagnosticsEnglishVsTranslated`:
  - `translation_unavailable`
  - RU ranking misses (`ranking_miss_top10`)
  - Translation-vs-RU pipeline contribution counters.

## Decision loop: implementation -> verification -> keep or rollback
1. Implementation
- Make exactly one isolated change hypothesis (one idea per iteration).
- Keep change scope minimal to attribute effect correctly.

2. Verification
- Run baseline before change and save logs:
`go test -tags quality -count=1 -run 'TestSearchQualityCrossLanguageComparison$|TestSearchQualityCrossLanguageHoldout$|TestSearchDiagnosticsEnglishVsTranslated$' -v | Tee-Object baseline.log`
- Apply change.
- Run the same test set and save logs:
`go test -tags quality -count=1 -run 'TestSearchQualityCrossLanguageComparison$|TestSearchQualityCrossLanguageHoldout$|TestSearchDiagnosticsEnglishVsTranslated$' -v | Tee-Object candidate.log`

3. Keep or rollback
- Keep the change if all conditions are true:
  - RU quality improved on primary metrics (`mrr`, `ndcg`, `hit@10`) in main or holdout suite.
  - EN quality did not regress materially.
  - RU/EN ratio did not degrade.
  - No new stability issues in diagnostics (`translation_unavailable` or large ranking-miss growth).
  - Efficiency is not worse, or trade-off is explicitly accepted.
- Roll back the change if any condition above fails.

## Efficiency check
- Compare wall-clock test time from `go test` output (`ok asktg <seconds>`).
- Compare warning rates in logs (`query translation failed`, `embedding failed`).
- If quality gain is tiny but latency/instability grows, prefer rollback.

## Example: translator model comparison on same provider
1. Set model in DB settings:
`sqlite3 "$env:LOCALAPPDATA\\asktg\\app.db" "INSERT INTO settings(key,value) VALUES('query_translation_model','<model>') ON CONFLICT(key) DO UPDATE SET value=excluded.value;"`
2. Run quality suite and save per-model log.
3. Repeat for each model.
4. Select model by combined score:
- Highest RU/EN ratio on `mrr` and `ndcg`.
- No catastrophic holdout regressions.
- Lowest `translation_unavailable` in diagnostics.

## Notes
- Run with `-count=1` to avoid cached test results.
- Keep dataset constant during comparisons.
- Do not compare logs from different index snapshots as if they were equivalent.
