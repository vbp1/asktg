//go:build quality
// +build quality

package main

import (
	"fmt"
	"testing"

	"asktg/internal/domain"
)

type pipelineSnapshot struct {
	PreRerank       []domain.SearchResult
	Final           []domain.SearchResult
	Candidates      map[string]struct{}
	TranslatedQuery string
}

type diagnosticVariantStats struct {
	Evaluated     int
	Hit10         int
	MRR           float64
	NDCG          float64
	RetrievalMiss int
	RankingMiss10 int
}

func (s *diagnosticVariantStats) Observe(rank int, retrieved bool, topK int) {
	s.Evaluated++
	if rank > 0 && rank <= topK {
		s.Hit10++
	}
	s.MRR += reciprocalRank(rank)
	s.NDCG += ndcgAt(20, rank)
	if !retrieved {
		s.RetrievalMiss++
		return
	}
	if rank == 0 || rank > topK {
		s.RankingMiss10++
	}
}

func TestSearchDiagnosticsEnglishVsTranslated(t *testing.T) {
	app := openQualityEvalApp(t)
	const (
		topK       = 10
		finalLimit = 20
	)

	cases := diagnosticCases()
	if len(cases) == 0 {
		t.Fatal("no diagnostic cases")
	}

	enStats := diagnosticVariantStats{}
	trStats := diagnosticVariantStats{}
	ruStats := diagnosticVariantStats{}

	translationUnavailable := 0
	enCeilingMiss := 0
	translationPenalty := 0
	ruPipelinePenalty := 0
	ruPipelineRecovery := 0

	for _, tc := range cases {
		if _, err := app.GetMessage(tc.ChatID, tc.MsgID); err != nil {
			t.Logf("skip missing target case=%q chat=%d msg=%d err=%v", tc.Name, tc.ChatID, tc.MsgID, err)
			continue
		}

		translatedEN := app.translateQueryRUToEN(app.ctx, tc.QueryRU)
		if translatedEN == "" {
			translationUnavailable++
			t.Logf("skip case=%q reason=no_translation ruQ=%q", tc.Name, tc.QueryRU)
			continue
		}

		enSnapshot, err := buildPipelineSnapshot(app, tc.QueryEN, finalLimit, false)
		if err != nil {
			t.Fatalf("snapshot EN case=%q: %v", tc.Name, err)
		}
		trSnapshot, err := buildPipelineSnapshot(app, translatedEN, finalLimit, false)
		if err != nil {
			t.Fatalf("snapshot translated EN case=%q: %v", tc.Name, err)
		}
		ruSnapshot, err := buildPipelineSnapshot(app, tc.QueryRU, finalLimit, true)
		if err != nil {
			t.Fatalf("snapshot RU case=%q: %v", tc.Name, err)
		}

		target := resultKey(tc.ChatID, tc.MsgID)
		enRank := rankOfTarget(enSnapshot.Final, tc.ChatID, tc.MsgID)
		trRank := rankOfTarget(trSnapshot.Final, tc.ChatID, tc.MsgID)
		ruRank := rankOfTarget(ruSnapshot.Final, tc.ChatID, tc.MsgID)

		_, enRetrieved := enSnapshot.Candidates[target]
		_, trRetrieved := trSnapshot.Candidates[target]
		_, ruRetrieved := ruSnapshot.Candidates[target]

		enStats.Observe(enRank, enRetrieved, topK)
		trStats.Observe(trRank, trRetrieved, topK)
		ruStats.Observe(ruRank, ruRetrieved, topK)

		enHit := enRank > 0 && enRank <= topK
		trHit := trRank > 0 && trRank <= topK
		ruHit := ruRank > 0 && ruRank <= topK

		if !enHit {
			enCeilingMiss++
		}
		if enHit && !trHit {
			translationPenalty++
		}
		if trHit && !ruHit {
			ruPipelinePenalty++
		}
		if !trHit && ruHit {
			ruPipelineRecovery++
		}

		t.Logf(
			"diag case=%q target=%d/%d enRank=%d trRank=%d ruRank=%d enRetrieved=%t trRetrieved=%t ruRetrieved=%t translated=%q",
			tc.Name, tc.ChatID, tc.MsgID, enRank, trRank, ruRank, enRetrieved, trRetrieved, ruRetrieved, translatedEN,
		)
	}

	if enStats.Evaluated < 12 {
		t.Fatalf("not enough evaluated diagnostic cases: got=%d want>=12", enStats.Evaluated)
	}

	logDiagnosticStats(t, "EN(original)", enStats, topK)
	logDiagnosticStats(t, "EN(translated_from_RU)", trStats, topK)
	logDiagnosticStats(t, "RU(full_pipeline)", ruStats, topK)

	t.Logf(
		"diag contributions: evaluated=%d translation_unavailable=%d en_ceiling_miss=%d translation_penalty=%d ru_pipeline_penalty=%d ru_pipeline_recovery=%d",
		enStats.Evaluated, translationUnavailable, enCeilingMiss, translationPenalty, ruPipelinePenalty, ruPipelineRecovery,
	)
	if enStats.Evaluated > 0 {
		missEN := float64(enStats.Evaluated-enStats.Hit10) / float64(enStats.Evaluated)
		missTR := float64(trStats.Evaluated-trStats.Hit10) / float64(trStats.Evaluated)
		missRU := float64(ruStats.Evaluated-ruStats.Hit10) / float64(ruStats.Evaluated)
		t.Logf(
			"diag miss@%d rates: EN=%.3f translatedEN=%.3f RU=%.3f",
			topK, missEN, missTR, missRU,
		)
	}
}

type stageVariantStats struct {
	Evaluated         int
	CandidateHit      int
	PreHit10          int
	PostHit10         int
	PreMRR            float64
	PostMRR           float64
	PreNDCG           float64
	PostNDCG          float64
	PreRankingMiss10  int
	PostRankingMiss10 int
}

func (s *stageVariantStats) Observe(preRank int, postRank int, candidateHit bool, topK int) {
	s.Evaluated++
	if candidateHit {
		s.CandidateHit++
	}
	if preRank > 0 && preRank <= topK {
		s.PreHit10++
	} else if candidateHit {
		s.PreRankingMiss10++
	}
	if postRank > 0 && postRank <= topK {
		s.PostHit10++
	} else if candidateHit {
		s.PostRankingMiss10++
	}
	s.PreMRR += reciprocalRank(preRank)
	s.PostMRR += reciprocalRank(postRank)
	s.PreNDCG += ndcgAt(20, preRank)
	s.PostNDCG += ndcgAt(20, postRank)
}

func TestSearchDiagnosticsStageMetrics(t *testing.T) {
	app := openQualityEvalApp(t)
	const topK = 10

	cases := diagnosticCases()
	en := stageVariantStats{}
	ru := stageVariantStats{}

	for _, tc := range cases {
		if _, err := app.GetMessage(tc.ChatID, tc.MsgID); err != nil {
			t.Logf("skip missing stage case=%q chat=%d msg=%d err=%v", tc.Name, tc.ChatID, tc.MsgID, err)
			continue
		}
		target := resultKey(tc.ChatID, tc.MsgID)

		enSnapshot, err := buildPipelineSnapshot(app, tc.QueryEN, 20, false)
		if err != nil {
			t.Fatalf("stage EN snapshot case=%q: %v", tc.Name, err)
		}
		enPre := rankOfTarget(enSnapshot.PreRerank, tc.ChatID, tc.MsgID)
		enPost := rankOfTarget(enSnapshot.Final, tc.ChatID, tc.MsgID)
		_, enCandidate := enSnapshot.Candidates[target]
		en.Observe(enPre, enPost, enCandidate, topK)

		ruSnapshot, err := buildPipelineSnapshot(app, tc.QueryRU, 20, true)
		if err != nil {
			t.Fatalf("stage RU snapshot case=%q: %v", tc.Name, err)
		}
		ruPre := rankOfTarget(ruSnapshot.PreRerank, tc.ChatID, tc.MsgID)
		ruPost := rankOfTarget(ruSnapshot.Final, tc.ChatID, tc.MsgID)
		_, ruCandidate := ruSnapshot.Candidates[target]
		ru.Observe(ruPre, ruPost, ruCandidate, topK)
	}

	if en.Evaluated < 12 || ru.Evaluated < 12 {
		t.Fatalf("not enough stage-evaluated cases: en=%d ru=%d", en.Evaluated, ru.Evaluated)
	}
	logStageStats(t, "EN", en, topK)
	logStageStats(t, "RU", ru, topK)

	enCandidateRecall := float64(en.CandidateHit) / float64(en.Evaluated)
	ruCandidateRecall := float64(ru.CandidateHit) / float64(ru.Evaluated)
	enPostHit := float64(en.PostHit10) / float64(en.Evaluated)
	ruPreHit := float64(ru.PreHit10) / float64(ru.Evaluated)
	ruPostHit := float64(ru.PostHit10) / float64(ru.Evaluated)

	if enCandidateRecall < 0.90 {
		t.Fatalf("EN candidate recall too low: %.3f", enCandidateRecall)
	}
	if ruCandidateRecall < 0.90 {
		t.Fatalf("RU candidate recall too low: %.3f", ruCandidateRecall)
	}
	if enPostHit < 0.70 {
		t.Fatalf("EN post-rerank hit@%d too low: %.3f", topK, enPostHit)
	}
	if ruPostHit < ruPreHit*0.80 {
		t.Fatalf("RU post-rerank degraded too much: pre=%.3f post=%.3f", ruPreHit, ruPostHit)
	}
}

func diagnosticCases() []crossLangCase {
	seen := make(map[string]struct{}, len(crossLangCases)+len(holdoutCrossLangCases))
	out := make([]crossLangCase, 0, len(crossLangCases)+len(holdoutCrossLangCases))
	consume := func(items []crossLangCase) {
		for _, tc := range items {
			key := resultKey(tc.ChatID, tc.MsgID)
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			out = append(out, tc)
		}
	}
	consume(crossLangCases)
	consume(holdoutCrossLangCases)
	return out
}

func buildPipelineSnapshot(app *App, query string, finalLimit int, includeRUTranslation bool) (pipelineSnapshot, error) {
	req := domain.SearchRequest{
		Query: query,
		Mode:  domain.SearchModeHybrid,
		Filters: domain.SearchFilters{
			Limit: finalLimit,
		},
	}
	reqUnlimited := req
	reqUnlimited.Filters.Limit = -1

	ftsFinal, err := app.store.Search(app.ctx, req)
	if err != nil {
		return pipelineSnapshot{}, err
	}
	markFTS(ftsFinal)
	ftsCandidates, err := app.store.Search(app.ctx, reqUnlimited)
	if err != nil {
		return pipelineSnapshot{}, err
	}
	markFTS(ftsCandidates)

	translatedQuery := ""
	if includeRUTranslation {
		translatedQuery = app.translateQueryRUToEN(app.ctx, query)
		if translatedQuery != "" {
			translatedReq := req
			translatedReq.Query = translatedQuery
			translatedFTS, translatedErr := app.store.Search(app.ctx, translatedReq)
			if translatedErr != nil {
				app.safeLogWarningf("diagnostic translated FTS search failed: %v", translatedErr)
			} else if len(translatedFTS) > 0 {
				markFTS(translatedFTS)
				ftsFinal = mergeRankedResultsByRRFWeighted(ftsFinal, translatedFTS, req.Filters.Limit, 1.0, 2.0)
			}

			translatedReqUnlimited := reqUnlimited
			translatedReqUnlimited.Query = translatedQuery
			translatedFTSCandidates, translatedErr := app.store.Search(app.ctx, translatedReqUnlimited)
			if translatedErr != nil {
				app.safeLogWarningf("diagnostic translated FTS candidate search failed: %v", translatedErr)
			} else if len(translatedFTSCandidates) > 0 {
				markFTS(translatedFTSCandidates)
				ftsCandidates = append(ftsCandidates, translatedFTSCandidates...)
			}
		}
	}

	vectorFinal := []domain.SearchResult{}
	vectorCandidates := []domain.SearchResult{}

	if app.embedClient != nil && app.embedClient.Configured() && app.vectorIndex != nil {
		enabled, scopeErr := app.hasEmbeddingsScope(app.ctx, req.Filters.ChatIDs)
		if scopeErr != nil {
			app.safeLogWarningf("diagnostic embeddings scope lookup failed: %v", scopeErr)
		}
		if scopeErr == nil && enabled {
			embeddingQueries := []string{query}
			if includeRUTranslation && translatedQuery != "" {
				embeddingQueries = append(embeddingQueries, translatedQuery)
			}
			vectors, embedErr := app.embedClient.Embed(app.ctx, embeddingQueries)
			if embedErr != nil {
				app.safeLogWarningf("diagnostic query embedding failed: %v", embedErr)
			} else if len(vectors) > 0 {
				candidates := app.vectorIndex.Search(vectors[0], 300)
				for idx := 1; idx < len(vectors); idx++ {
					if len(vectors[idx]) == 0 {
						continue
					}
					additional := app.vectorIndex.Search(vectors[idx], 300)
					candidates = mergeVectorCandidates(candidates, additional, 300)
				}
				candidates = filterSemanticCandidates(candidates, app.semanticProfile(app.ctx))
				if len(candidates) > 0 {
					vectorFinal, err = app.lookupVectorCandidates(app.ctx, req, candidates)
					if err != nil {
						return pipelineSnapshot{}, err
					}
					vectorCandidates, err = app.lookupVectorCandidates(app.ctx, reqUnlimited, candidates)
					if err != nil {
						return pipelineSnapshot{}, err
					}
				}
			}
		}
	}

	final := fuseByRRF(ftsFinal, vectorFinal, req.Filters.Limit)
	preRerank := append([]domain.SearchResult(nil), final...)
	if includeRUTranslation && translatedQuery != "" {
		preRerank = fuseByRRFWeighted(ftsFinal, vectorFinal, req.Filters.Limit, 1.7, 1.0)
		final = rerankByAnchorCoverage(preRerank, translatedQuery, req.Filters.Limit)
		preRerank = append([]domain.SearchResult(nil), preRerank...)
	}

	candidateSet := make(map[string]struct{}, len(ftsCandidates)+len(vectorCandidates))
	for _, item := range ftsCandidates {
		candidateSet[resultKey(item.ChatID, item.MsgID)] = struct{}{}
	}
	for _, item := range vectorCandidates {
		candidateSet[resultKey(item.ChatID, item.MsgID)] = struct{}{}
	}

	return pipelineSnapshot{
		PreRerank:       preRerank,
		Final:           final,
		Candidates:      candidateSet,
		TranslatedQuery: translatedQuery,
	}, nil
}

func markFTS(items []domain.SearchResult) {
	for idx := range items {
		items[idx].MatchFTS = true
	}
}

func logDiagnosticStats(t *testing.T, label string, stats diagnosticVariantStats, topK int) {
	t.Helper()
	if stats.Evaluated == 0 {
		t.Logf("diag %s: no evaluated cases", label)
		return
	}
	hit := float64(stats.Hit10) / float64(stats.Evaluated)
	mrr := stats.MRR / float64(stats.Evaluated)
	ndcg := stats.NDCG / float64(stats.Evaluated)
	retrievalMissRate := float64(stats.RetrievalMiss) / float64(stats.Evaluated)
	rankingMissRate := float64(stats.RankingMiss10) / float64(stats.Evaluated)
	t.Logf(
		"diag %s: eval=%d hit@%d=%.3f mrr=%.3f ndcg@20=%.3f retrieval_miss=%d(%.3f) ranking_miss_top%d=%d(%.3f)",
		label,
		stats.Evaluated,
		topK,
		hit,
		mrr,
		ndcg,
		stats.RetrievalMiss,
		retrievalMissRate,
		topK,
		stats.RankingMiss10,
		rankingMissRate,
	)
	t.Logf("diag %s: miss_breakdown=%s", label, fmt.Sprintf("retrieval=%d ranking=%d", stats.RetrievalMiss, stats.RankingMiss10))
}

func logStageStats(t *testing.T, label string, stats stageVariantStats, topK int) {
	t.Helper()
	preHit := float64(stats.PreHit10) / float64(stats.Evaluated)
	postHit := float64(stats.PostHit10) / float64(stats.Evaluated)
	preMRR := stats.PreMRR / float64(stats.Evaluated)
	postMRR := stats.PostMRR / float64(stats.Evaluated)
	preNDCG := stats.PreNDCG / float64(stats.Evaluated)
	postNDCG := stats.PostNDCG / float64(stats.Evaluated)
	candidateRecall := float64(stats.CandidateHit) / float64(stats.Evaluated)
	t.Logf(
		"stage %s: eval=%d candidate_recall=%.3f pre_hit@%d=%.3f post_hit@%d=%.3f pre_mrr=%.3f post_mrr=%.3f pre_ndcg=%.3f post_ndcg=%.3f pre_rank_miss=%d post_rank_miss=%d",
		label,
		stats.Evaluated,
		candidateRecall,
		topK,
		preHit,
		topK,
		postHit,
		preMRR,
		postMRR,
		preNDCG,
		postNDCG,
		stats.PreRankingMiss10,
		stats.PostRankingMiss10,
	)
}
