package repository

import (
	"context"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// 窗口起点固定：命中窗口的重复浏览不得前移 last_view_at，否则「连续刷」会把
// 同一个窗口无限延长，去重窗口就形同虚设。
func TestBumpPageViewKeepsWindowStartFixed(t *testing.T) {
	db, repo := newMasteryDedupDB(t, "mastery-window-start")
	ctx := context.Background()
	scope := interfaces.MemoryScope{TenantID: 7, SubjectID: "web_user:alice"}

	if err := repo.BumpPageView(ctx, scope, "kb-1", "entity/acme", 5); err != nil {
		t.Fatalf("first view: %v", err)
	}
	first := loadPageView(t, db, "web_user:alice", "entity/acme")

	if err := repo.BumpPageView(ctx, scope, "kb-1", "entity/acme", 5); err != nil {
		t.Fatalf("second view: %v", err)
	}
	second := loadPageView(t, db, "web_user:alice", "entity/acme")

	if second.ViewCount != 1 {
		t.Errorf("窗口内重复浏览不应增加次数，got view_count=%d", second.ViewCount)
	}
	if !second.LastViewAt.Equal(first.LastViewAt) {
		t.Errorf("命中窗口不得前移起点：first=%v second=%v", first.LastViewAt, second.LastViewAt)
	}
	if second.TotalDuration != 10 {
		t.Errorf("时长仍应累计，got total_duration=%d（want 10）", second.TotalDuration)
	}
}

// 汇总行的次数与日桶的次数必须一致。两者若采用不同口径（滚动 24 小时 vs 日历日），
// 就会出现「画像显示 5 次却已满格」——评分读日桶，展示读汇总行，用户会看到自相矛盾
// 的画像。这条把两个口径钉成同一个。
func TestBumpPageViewCountMatchesDailyBucketCount(t *testing.T) {
	db, repo := newMasteryDedupDB(t, "mastery-count-consistency")
	ctx := context.Background()
	scope := interfaces.MemoryScope{TenantID: 7, SubjectID: "web_user:alice"}

	const days = 5
	for i := 0; i < days; i++ {
		if err := repo.BumpPageView(ctx, scope, "kb-1", "entity/acme", 5); err != nil {
			t.Fatalf("view #%d: %v", i, err)
		}
		// 把这次浏览连同它的日桶一起挪到 (i+1) 天前，形成「每天来一次」的历史。
		when := time.Now().AddDate(0, 0, -(i + 1))
		if err := db.Model(&types.MemoryPageView{}).
			Where("subject_id = ? AND slug = ?", "web_user:alice", "entity/acme").
			Update("last_view_at", when).Error; err != nil {
			t.Fatalf("age view #%d: %v", i, err)
		}
		if err := db.Model(&types.MemoryMasteryDaily{}).
			Where("slug = ? AND event_type = ? AND event_date = ?", "entity/acme",
				types.MasteryEventView, time.Now().Format("2006-01-02")).
			Update("event_date", when.Format("2006-01-02")).Error; err != nil {
			t.Fatalf("age bucket #%d: %v", i, err)
		}
	}

	row := loadPageView(t, db, "web_user:alice", "entity/acme")
	var buckets []types.MemoryMasteryDaily
	if err := db.Where("slug = ? AND event_type = ?", "entity/acme", types.MasteryEventView).
		Find(&buckets).Error; err != nil {
		t.Fatalf("load buckets: %v", err)
	}
	bucketCount := 0
	for _, b := range buckets {
		bucketCount += b.EventCount
	}
	if row.ViewCount != days {
		t.Errorf("每天一次、共 %d 天应记 %d 次，got view_count=%d", days, days, row.ViewCount)
	}
	if len(buckets) != days || bucketCount != row.ViewCount {
		t.Errorf("汇总行与日桶必须同口径：view_count=%d 桶数=%d 桶次数和=%d",
			row.ViewCount, len(buckets), bucketCount)
	}
}

// 日桶按 cutoff 分成「近期逐日」与「冷端按 slug 折叠」两段；冷端的求和必须
// 覆盖该 slug 的全部旧桶，否则折叠会丢证据。
func TestListDailyViewsSplitsRecentAndCold(t *testing.T) {
	db, repo := newMasteryDedupDB(t, "mastery-daily-split")
	ctx := context.Background()
	scope := interfaces.MemoryScope{TenantID: 7, SubjectID: "web_user:alice"}

	day := func(offset int) string {
		return time.Now().AddDate(0, 0, offset).Format("2006-01-02")
	}
	seed := func(slug, date string, count int, duration int64) {
		t.Helper()
		row := &types.MemoryMasteryDaily{
			ID:              "daily-" + slug + "-" + date,
			TenantID:        scope.TenantID,
			SubjectID:       scope.SubjectID,
			KnowledgeBaseID: "kb-1",
			Slug:            slug,
			EventType:       types.MasteryEventView,
			EventDate:       date,
			EventCount:      count,
			DurationSum:     duration,
		}
		if err := db.Create(row).Error; err != nil {
			t.Fatalf("seed %s %s: %v", slug, date, err)
		}
	}

	seed("entity/acme", day(-1), 1, 30)
	seed("entity/acme", day(-2), 1, 60)
	seed("entity/acme", day(-100), 1, 300)
	seed("concept/other", day(-200), 1, 60)
	seed("concept/other", day(-201), 1, 120)

	recent, cold, err := repo.ListDailyViews(ctx, scope, "kb-1", time.Now().AddDate(0, 0, -30))
	if err != nil {
		t.Fatalf("list daily views: %v", err)
	}
	if len(recent) != 2 {
		t.Errorf("30 天内的桶应逐日保留，got %d 条", len(recent))
	}
	if len(cold) != 2 {
		t.Errorf("30 天前的桶应按 slug 折叠成 2 条，got %d 条", len(cold))
	}
	sums := make(map[string][2]int64, len(cold))
	for _, c := range cold {
		sums[c.Slug] = [2]int64{int64(c.Count), c.Seconds}
	}
	if got := sums["entity/acme"]; got != [2]int64{1, 300} {
		t.Errorf("entity/acme 的冷端折叠应为 {1,300}，got %v", got)
	}
	if got := sums["concept/other"]; got != [2]int64{2, 180} {
		t.Errorf("concept/other 的冷端折叠应为 {2,180}，got %v", got)
	}
	assertBucketDateContract(t, recent, cold)
}

// 曝光去重按服务器本地日历日判定，与日桶的 event_date 同口径：
// 昨天的展示不算「今天」，今天的展示才算。
func TestHasExposureTodayUsesLocalDay(t *testing.T) {
	db, repo := newMasteryDedupDB(t, "mastery-exposure-day")
	ctx := context.Background()
	scope := interfaces.MemoryScope{TenantID: 7, SubjectID: "web_user:alice"}

	now := time.Now()
	dayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())

	seed := func(candidate string, shownAt time.Time) {
		t.Helper()
		row := &types.MemoryGuideExposure{
			ID:              "exp-" + candidate,
			TenantID:        scope.TenantID,
			SubjectID:       scope.SubjectID,
			KnowledgeBaseID: "kb-1",
			TriggerSlug:     "entity/hub",
			CandidateSlug:   candidate,
			Strategy:        types.GuideStrategyPPRBoundary,
			Rank:            1,
			ShownAt:         shownAt,
		}
		if err := db.Create(row).Error; err != nil {
			t.Fatalf("seed exposure %s: %v", candidate, err)
		}
	}

	// 用「本地零点 ± 1 分钟」构造边界，避免测试在凌晨运行时把 -2h 算到昨天。
	seed("entity/today", dayStart.Add(time.Minute))
	seed("entity/yesterday", dayStart.Add(-time.Minute))

	got, err := repo.HasExposureToday(ctx, scope, "kb-1", "entity/hub", "entity/today")
	if err != nil {
		t.Fatalf("check today: %v", err)
	}
	if !got {
		t.Errorf("当天展示过的候选应判定为已展示")
	}

	got, err = repo.HasExposureToday(ctx, scope, "kb-1", "entity/hub", "entity/yesterday")
	if err != nil {
		t.Fatalf("check yesterday: %v", err)
	}
	if got {
		t.Errorf("昨天的展示不应算作「今天」")
	}
}
