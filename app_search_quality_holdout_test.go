//go:build quality
// +build quality

package main

import (
	"testing"

	"asktg/internal/domain"
)

var holdoutCrossLangCases = []crossLangCase{
	{Name: "pg_stat_plans extension", ChatID: -1003799435561, MsgID: 438, QueryEN: "pg_stat_plans postgres extension", QueryRU: "расширение pg_stat_plans postgres"},
	{Name: "pdu recovery utility", ChatID: -1003799435561, MsgID: 437, QueryEN: "postgres data recovery utility pdu", QueryRU: "утилита восстановления данных postgres pdu"},
	{Name: "postgresql performance blog", ChatID: -1003799435561, MsgID: 376, QueryEN: "postgresql performance proopensource", QueryRU: "производительность postgresql proopensource"},
	{Name: "dbtune postgresql", ChatID: -1003799435561, MsgID: 373, QueryEN: "dbtune automated tuning postgresql", QueryRU: "автоматическая настройка postgresql dbtune"},
	{Name: "mysql vs postgresql ha", ChatID: -1003799435561, MsgID: 375, QueryEN: "mysql ha vs postgresql ha", QueryRU: "высокая доступность mysql postgresql"},
	{Name: "innodb full page write", ChatID: -1003799435561, MsgID: 379, QueryEN: "innodb full page write postgres", QueryRU: "full page write innodb postgres"},
	{Name: "openai code with agents", ChatID: -1003799435561, MsgID: 425, QueryEN: "openai agents write code", QueryRU: "openai агенты пишут код"},
	{Name: "openai harness ru page", ChatID: -1003799435561, MsgID: 431, QueryEN: "openai harness engineering", QueryRU: "openai harness engineering статья"},
	{Name: "ouroboros repo", ChatID: -1003799435561, MsgID: 449, QueryEN: "ouroboros github", QueryRU: "репозиторий ouroboros github"},
	{Name: "data formulator", ChatID: -1003799435561, MsgID: 435, QueryEN: "microsoft data formulator", QueryRU: "microsoft data formulator"},
}

func TestSearchQualityCrossLanguageHoldout(t *testing.T) {
	app := openQualityEvalApp(t)
	limit := 20

	metrics := evalMetrics{}
	for _, tc := range holdoutCrossLangCases {
		if _, err := app.GetMessage(tc.ChatID, tc.MsgID); err != nil {
			t.Logf("skip missing holdout target %s chat=%d msg=%d err=%v", tc.Name, tc.ChatID, tc.MsgID, err)
			continue
		}

		enResults, err := app.searchMessages(app.ctx, searchReq(tc.QueryEN, limit))
		if err != nil {
			t.Fatalf("holdout search EN %s: %v", tc.Name, err)
		}
		ruResults, err := app.searchMessages(app.ctx, searchReq(tc.QueryRU, limit))
		if err != nil {
			t.Fatalf("holdout search RU %s: %v", tc.Name, err)
		}

		enRank := rankOfTarget(enResults, tc.ChatID, tc.MsgID)
		ruRank := rankOfTarget(ruResults, tc.ChatID, tc.MsgID)

		metrics.Evaluated++
		if enRank > 0 && enRank <= 5 {
			metrics.ENHit5++
		}
		if enRank > 0 && enRank <= 10 {
			metrics.ENHit10++
		}
		metrics.ENMRR += reciprocalRank(enRank)
		metrics.ENNDCG += ndcgAt(limit, enRank)

		if ruRank > 0 && ruRank <= 5 {
			metrics.RUHit5++
		}
		if ruRank > 0 && ruRank <= 10 {
			metrics.RUHit10++
		}
		metrics.RUMRR += reciprocalRank(ruRank)
		metrics.RUNDCG += ndcgAt(limit, ruRank)

		t.Logf(
			"holdout case=%q target=%d/%d enRank=%d ruRank=%d enQ=%q ruQ=%q",
			tc.Name, tc.ChatID, tc.MsgID, enRank, ruRank, tc.QueryEN, tc.QueryRU,
		)
	}

	if metrics.Evaluated < 6 {
		t.Fatalf("not enough evaluated holdout cases: got=%d want>=6", metrics.Evaluated)
	}

	enHit10 := float64(metrics.ENHit10) / float64(metrics.Evaluated)
	enMRR := metrics.ENMRR / float64(metrics.Evaluated)
	enNDCG := metrics.ENNDCG / float64(metrics.Evaluated)
	ruHit10 := float64(metrics.RUHit10) / float64(metrics.Evaluated)
	ruMRR := metrics.RUMRR / float64(metrics.Evaluated)
	ruNDCG := metrics.RUNDCG / float64(metrics.Evaluated)

	t.Logf("holdout evaluated=%d", metrics.Evaluated)
	t.Logf("holdout EN hit@10=%.3f mrr=%.3f ndcg@%d=%.3f", enHit10, enMRR, limit, enNDCG)
	t.Logf("holdout RU hit@10=%.3f mrr=%.3f ndcg@%d=%.3f", ruHit10, ruMRR, limit, ruNDCG)
	t.Logf("holdout RU/EN ratio hit@10=%.3f mrr=%.3f ndcg=%.3f", safeRatio(ruHit10, enHit10), safeRatio(ruMRR, enMRR), safeRatio(ruNDCG, enNDCG))

	if enHit10 < 0.50 {
		t.Fatalf("holdout EN baseline too low: hit@10=%.3f", enHit10)
	}
	if ruHit10 < 0.40 {
		t.Fatalf("holdout RU quality too low: hit@10=%.3f", ruHit10)
	}
	if safeRatio(ruHit10, enHit10) < 0.60 {
		t.Fatalf("holdout RU/EN parity too low: ru=%.3f en=%.3f ratio=%.3f", ruHit10, enHit10, safeRatio(ruHit10, enHit10))
	}
}

func searchReq(query string, limit int) domain.SearchRequest {
	return domain.SearchRequest{
		Query: query,
		Mode:  domain.SearchModeHybrid,
		Filters: domain.SearchFilters{
			Limit: limit,
		},
	}
}
