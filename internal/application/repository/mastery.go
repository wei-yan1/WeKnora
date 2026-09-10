package repository

import (
	"context"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// masteryRepository stores the knowledge-guidance ledgers. It mirrors
// memoryRepository's scoping and upsert discipline so every row is
// (tenant_id, subject_id)-isolated and writes are idempotent under retry.
type masteryRepository struct {
	db *gorm.DB
}

// NewMasteryRepository creates the knowledge-guidance ledger repository.
func NewMasteryRepository(db *gorm.DB) interfaces.MasteryRepository {
	return &masteryRepository{db: db}
}

func (r *masteryRepository) scoped(ctx context.Context, scope interfaces.MemoryScope) *gorm.DB {
	return r.db.WithContext(ctx).
		Where("tenant_id = ? AND subject_id = ?", scope.TenantID, scope.SubjectID)
}

// BumpCitation records cited docs in the guidance ledger. It mirrors
// memoryRepository.BumpDocAffinity's insert-then-increment shape for idempotency.
func (r *masteryRepository) BumpCitation(
	ctx context.Context, scope interfaces.MemoryScope, docs []types.MemoryDocAffinity,
) error {
	now := time.Now()
	for _, doc := range docs {
		if doc.KnowledgeID == "" {
			continue
		}
		row := &types.MemoryCitation{
			ID:              uuid.New().String(),
			TenantID:        scope.TenantID,
			SubjectID:       scope.SubjectID,
			KnowledgeID:     doc.KnowledgeID,
			KnowledgeBaseID: doc.KnowledgeBaseID,
			LastCitedAt:     now,
		}
		if err := r.db.WithContext(ctx).
			Clauses(clause.OnConflict{
				Columns: []clause.Column{
					{Name: "tenant_id"}, {Name: "subject_id"}, {Name: "knowledge_id"},
				},
				DoNothing: true,
			}).
			Create(row).Error; err != nil {
			return err
		}
		updates := map[string]interface{}{
			"cite_count":    gorm.Expr("cite_count + 1"),
			"last_cited_at": now,
			"updated_at":    now,
		}
		if doc.KnowledgeBaseID != "" {
			updates["knowledge_base_id"] = doc.KnowledgeBaseID
		}
		if err := r.scoped(ctx, scope).
			Model(&types.MemoryCitation{}).
			Where("knowledge_id = ?", doc.KnowledgeID).
			Updates(updates).Error; err != nil {
			return err
		}
		// 日聚合：citation 事件按文档 knowledge_id 记账。
		if err := r.bumpDaily(ctx, scope, doc.KnowledgeBaseID, doc.KnowledgeID, types.MasteryEventCitation, 1, 0); err != nil {
			return err
		}
	}
	return nil
}

// BumpPageView records one effective page view with its duration.
//
// The window verdict is evaluated by the database inside a single upsert, not by
// a preceding SELECT. The old shape (count → decide → increment) had a TOCTOU
// window: two concurrent views of a page whose window had just expired could both
// conclude "not repeated" and each add a count, so the folded row could gain two
// repetitions from one moment. Folding it into one statement also drops two
// round trips per view.
func (r *masteryRepository) BumpPageView(
	ctx context.Context, scope interfaces.MemoryScope, kbID, slug string, duration int64,
) error {
	if slug == "" {
		return nil
	}
	now := time.Now()
	// 同一天内只计一次：以服务器本地日历日为零点，与日聚合桶、曝光去重同口径。
	dayStart := types.MasteryPageViewDayStart(now)

	// 同页去重：同一节点在同一天内重复打开，只折叠「浏览次数」，「时长」照常累计。
	//
	// 只折次数、不折时长是刻意的：需要挡的是「反复开关页面刷次数」——6 次 × 5 秒
	// 的点击就能靠 ViewWeight 把节点推到 100%；但当天第二次认真阅读（几分钟）
	// 是真实的学习行为，把它丢掉会逼用户「过一会儿再来看一遍」才能涨水位，体验很差。
	// 时长权重本身偏低（每 60 秒 1 分），累计它不会破坏「跨天」的档位语义。
	//
	// 命中窗口时不前移 last_view_at：当天起点因此保持固定，连续刷不会把同一个
	// 计数窗口无限延长。判定改用 CASE 表达式后，起点与自增在同一条语句内完成。
	row := &types.MemoryPageView{
		ID:              uuid.New().String(),
		TenantID:        scope.TenantID,
		SubjectID:       scope.SubjectID,
		KnowledgeBaseID: kbID,
		Slug:            slug,
		ViewCount:       1,        // 首次插入即在窗口外，直接计一次
		TotalDuration:   duration, // 首次插入即带上本次时长
		LastViewAt:      now,
	}
	if err := r.db.WithContext(ctx).
		Clauses(clause.OnConflict{
			Columns: []clause.Column{
				{Name: "tenant_id"}, {Name: "subject_id"},
				{Name: "knowledge_base_id"}, {Name: "slug"},
			},
			// 表达式里的列必须带表限定：PostgreSQL 的 DO UPDATE 作用域里同时有
			// 目标表与 excluded，未限定名会报 "column reference is ambiguous"。
			DoUpdates: clause.Assignments(map[string]interface{}{
				"view_count": gorm.Expr(
					"memory_page_views.view_count + CASE WHEN memory_page_views.last_view_at >= ? THEN 0 ELSE 1 END",
					dayStart),
				"total_duration": gorm.Expr("memory_page_views.total_duration + ?", duration),
				"last_view_at": gorm.Expr(
					"CASE WHEN memory_page_views.last_view_at >= ? THEN memory_page_views.last_view_at ELSE ? END",
					dayStart, now),
				"updated_at": now,
			}),
		}).
		Create(row).Error; err != nil {
		return err
	}
	// 日聚合：view 事件按页面 slug 记账，支持时间切分回放与逐日衰减。次数与
	// view_count 同口径（按窗口折叠，且每天最多 1 次），时长照常累计。
	return r.bumpDailyOncePerDay(ctx, scope, kbID, slug, types.MasteryEventView, duration)
}

// bumpDaily upserts a daily aggregation bucket. The slug column stores the event
// subject: a page slug for views, a knowledge id for citations. It is the cheap
// event ledger that backs time-split replay and time-decay without a full log.
func (r *masteryRepository) bumpDaily(
	ctx context.Context, scope interfaces.MemoryScope, kbID, subject, eventType string, count int, duration int64,
) error {
	if subject == "" || eventType == "" {
		return nil
	}
	now := time.Now()
	date := now.Format("2006-01-02")
	row := &types.MemoryMasteryDaily{
		ID:              uuid.New().String(),
		TenantID:        scope.TenantID,
		SubjectID:       scope.SubjectID,
		KnowledgeBaseID: kbID,
		Slug:            subject,
		EventType:       eventType,
		EventDate:       date,
	}
	if err := r.db.WithContext(ctx).
		Clauses(clause.OnConflict{
			Columns: []clause.Column{
				{Name: "tenant_id"}, {Name: "subject_id"}, {Name: "knowledge_base_id"},
				{Name: "slug"}, {Name: "event_type"}, {Name: "event_date"},
			},
			DoNothing: true,
		}).
		Create(row).Error; err != nil {
		return err
	}
	updates := map[string]interface{}{
		"event_count": gorm.Expr("event_count + ?", count),
		"updated_at":  now,
	}
	if duration > 0 {
		updates["duration_sum"] = gorm.Expr("duration_sum + ?", duration)
	}
	return r.scoped(ctx, scope).
		Model(&types.MemoryMasteryDaily{}).
		Where("knowledge_base_id = ? AND slug = ? AND event_type = ? AND event_date = ?",
			kbID, subject, eventType, date).
		Updates(updates).Error
}

// bumpDailyOncePerDay upserts a daily bucket whose count is capped at one per
// (subject, day), with duration still accumulating. It exists so the view bucket
// can mirror BumpPageView's window rule without reading the page-view row back:
// BumpPageView dedups on the local calendar day, so its row counts at most once
// per local day — which is exactly what capping the bucket's own count at one per
// day expresses, evaluated atomically against the bucket's own row.
func (r *masteryRepository) bumpDailyOncePerDay(
	ctx context.Context, scope interfaces.MemoryScope, kbID, subject, eventType string, duration int64,
) error {
	if subject == "" || eventType == "" {
		return nil
	}
	now := time.Now()
	row := &types.MemoryMasteryDaily{
		ID:              uuid.New().String(),
		TenantID:        scope.TenantID,
		SubjectID:       scope.SubjectID,
		KnowledgeBaseID: kbID,
		Slug:            subject,
		EventType:       eventType,
		EventDate:       now.Format("2006-01-02"),
		EventCount:      1,
		DurationSum:     duration,
	}
	updates := map[string]interface{}{
		"event_count": gorm.Expr(
			"memory_mastery_daily.event_count + CASE WHEN memory_mastery_daily.event_count >= 1 THEN 0 ELSE 1 END"),
		"updated_at": now,
	}
	if duration > 0 {
		updates["duration_sum"] = gorm.Expr("memory_mastery_daily.duration_sum + ?", duration)
	}
	return r.db.WithContext(ctx).
		Clauses(clause.OnConflict{
			Columns: []clause.Column{
				{Name: "tenant_id"}, {Name: "subject_id"}, {Name: "knowledge_base_id"},
				{Name: "slug"}, {Name: "event_type"}, {Name: "event_date"},
			},
			DoUpdates: clause.Assignments(updates),
		}).
		Create(row).Error
}

// RecordAnswerLike upserts a like bound to a message, storing its allocation
// snapshot. Re-liking the same message overwrites the snapshot and clears the
// cancellation.
func (r *masteryRepository) RecordAnswerLike(
	ctx context.Context, scope interfaces.MemoryScope, like *types.MemoryAnswerLike,
) error {
	like.ID = uuid.New().String()
	like.TenantID = scope.TenantID
	like.SubjectID = scope.SubjectID
	like.LikedAt = time.Now()
	like.CancelledAt = nil
	if err := r.db.WithContext(ctx).
		Clauses(clause.OnConflict{
			Columns: []clause.Column{
				{Name: "tenant_id"}, {Name: "subject_id"}, {Name: "message_id"},
			},
			DoUpdates: clause.AssignmentColumns([]string{"allocations", "liked_at", "cancelled_at"}),
		}).
		Create(like).Error; err != nil {
		return err
	}
	// 日聚合：like 事件按来源文档 knowledge_id 记账，补齐时间切分回放的
	// 点赞维度。knowledge_id 全局唯一，KB 空也不与其它 KB 冲突；若分配携带
	// KB 则一并记录。
	for _, alloc := range like.Allocations {
		if err := r.bumpDaily(ctx, scope, alloc.KnowledgeBaseID, alloc.KnowledgeID, types.MasteryEventLike, 1, 0); err != nil {
			return err
		}
	}
	return nil
}

// CancelAnswerLike marks a like cancelled, so its allocations stop counting.
func (r *masteryRepository) CancelAnswerLike(
	ctx context.Context, scope interfaces.MemoryScope, messageID string,
) error {
	now := time.Now()
	return r.scoped(ctx, scope).
		Model(&types.MemoryAnswerLike{}).
		Where("message_id = ? AND cancelled_at IS NULL", messageID).
		Updates(map[string]interface{}{"cancelled_at": now}).Error
}

// RecordExposure writes one guidance exposure row.
func (r *masteryRepository) RecordExposure(
	ctx context.Context, scope interfaces.MemoryScope, exp *types.MemoryGuideExposure,
) error {
	exp.ID = uuid.New().String()
	exp.TenantID = scope.TenantID
	exp.SubjectID = scope.SubjectID
	return r.db.WithContext(ctx).Create(exp).Error
}

// HasExposureToday reports whether the scope already exposed a candidate for a
// given (kb, trigger) today, so repeated clicks on the same center don't spam
// the exposure log.
func (r *masteryRepository) HasExposureToday(
	ctx context.Context, scope interfaces.MemoryScope, kbID, triggerSlug, candidateSlug string,
) (bool, error) {
	// 本地日历零点，与日聚合桶的 event_date（同样是本地日期）保持一致。
	// time.Truncate(24h) 是按 UTC 边界切的，东八区会变成当地 08:00 换天。
	now := time.Now()
	dayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	var n int64
	err := r.scoped(ctx, scope).
		Model(&types.MemoryGuideExposure{}).
		Where("knowledge_base_id = ? AND trigger_slug = ? AND candidate_slug = ? AND shown_at >= ?",
			kbID, triggerSlug, candidateSlug, dayStart).
		Count(&n).Error
	return n > 0, err
}

// MarkExposureClicked marks the most recent unclicked exposure for a candidate
// in a KB as clicked (the candidate the user actually followed up on).
func (r *masteryRepository) MarkExposureClicked(
	ctx context.Context, scope interfaces.MemoryScope, kbID, candidateSlug string,
) error {
	now := time.Now()
	return r.scoped(ctx, scope).
		Model(&types.MemoryGuideExposure{}).
		Where("knowledge_base_id = ? AND candidate_slug = ? AND clicked_at IS NULL", kbID, candidateSlug).
		Order("shown_at DESC").
		Limit(1).
		Updates(map[string]interface{}{"clicked_at": now}).Error
}

// MarkExposureQualified marks the most recent unqualified exposure for a
// candidate in a KB as qualified: the candidate page reached the effective-view
// threshold, which is the reward signal for offline evaluation and strategy
// learning. Idempotent — only exposures without a qualified_view_at are touched.
func (r *masteryRepository) MarkExposureQualified(
	ctx context.Context, scope interfaces.MemoryScope, kbID, slug string,
) error {
	now := time.Now()
	return r.scoped(ctx, scope).
		Model(&types.MemoryGuideExposure{}).
		Where("knowledge_base_id = ? AND candidate_slug = ? AND qualified_view_at IS NULL", kbID, slug).
		Order("shown_at DESC").
		Limit(1).
		Updates(map[string]interface{}{"qualified_view_at": now}).Error
}

func (r *masteryRepository) ListCitations(
	ctx context.Context, scope interfaces.MemoryScope, kbID string,
) ([]*types.MemoryCitation, error) {
	var rows []*types.MemoryCitation
	err := r.scoped(ctx, scope).
		Where("knowledge_base_id = ?", kbID).
		Find(&rows).Error
	return rows, err
}

func (r *masteryRepository) ListPageViews(
	ctx context.Context, scope interfaces.MemoryScope, kbID string,
) ([]*types.MemoryPageView, error) {
	var rows []*types.MemoryPageView
	err := r.scoped(ctx, scope).
		Where("knowledge_base_id = ?", kbID).
		Find(&rows).Error
	return rows, err
}

// dailyLedger describes one per-day ledger read: which table, which extra filter
// (the daily table is keyed by event type, the spread table is single-purpose),
// and which columns carry the folded count and the seconds.
//
// It exists so the two daily reads share one query and one folding rule instead of
// two copies kept in sync by a comment promising they stay "shaped exactly like"
// each other.
type dailyLedger struct {
	model         interface{}
	filter        string
	filterArgs    []interface{}
	countColumn   string
	secondsColumn string
}

var (
	viewDailyLedger = dailyLedger{
		model:         &types.MemoryMasteryDaily{},
		filter:        "event_type = ?",
		filterArgs:    []interface{}{types.MasteryEventView},
		countColumn:   "event_count",
		secondsColumn: "duration_sum",
	}
	// Spread carries time only, so it contributes no count: SUM(0) keeps the read
	// shaped like the view read without inventing a column for it.
	spreadDailyLedger = dailyLedger{
		model:         &types.MemorySpreadView{},
		countColumn:   "0",
		secondsColumn: "spread_seconds",
	}
)

// listDailyBuckets returns the buckets of one KB, split at cutoff:
//
//	recent — one row per (slug, day) for days on or after cutoff, oldest first,
//	         so every recent day can pay its own decay;
//	cold   — one row per slug summing every day before cutoff.
//
// The split keeps the row count bounded (visited pages, instead of visited pages
// × days). Splitting at the decay floor's horizon makes it exact rather than an
// approximation: every cold bucket already decays at exactly DecayFloor, so one
// summed row per slug scores identically to the individual days. Cold rows come
// back with an empty EventDate — that is how the caller knows to stamp them with
// the cutoff day.
func (r *masteryRepository) listDailyBuckets(
	ctx context.Context, scope interfaces.MemoryScope, kbID string, ledger dailyLedger, cutoff time.Time,
) (recent, cold []types.MasteryDailyBucket, err error) {
	cutoffDate := cutoff.Format("2006-01-02")
	// A fresh chain per query: reusing one *gorm.DB would let the first statement's
	// WHERE clauses leak into the second.
	scoped := func() *gorm.DB {
		q := r.scoped(ctx, scope).Where("knowledge_base_id = ?", kbID)
		if ledger.filter != "" {
			q = q.Where(ledger.filter, ledger.filterArgs...)
		}
		return q
	}
	if err = scoped().Model(ledger.model).
		Select("slug, event_date, "+ledger.countColumn+" AS count, "+
			ledger.secondsColumn+" AS seconds").
		Where("event_date >= ?", cutoffDate).
		Order("event_date ASC").
		Find(&recent).Error; err != nil {
		return nil, nil, err
	}
	if err = scoped().Model(ledger.model).
		Select("slug, SUM("+ledger.countColumn+") AS count, SUM("+
			ledger.secondsColumn+") AS seconds").
		Where("event_date < ?", cutoffDate).
		Group("slug").
		Order("slug ASC").
		Find(&cold).Error; err != nil {
		return nil, nil, err
	}
	// 归一日期渲染，理由见 normalizeBucketDay。
	for i := range recent {
		recent[i].EventDate = normalizeBucketDay(recent[i].EventDate)
	}
	for i := range cold {
		cold[i].EventDate = normalizeBucketDay(cold[i].EventDate)
	}
	return recent, cold, nil
}

// normalizeBucketDay trims a bucket's date to its calendar-day part.
//
// event_date is a real DATE in PostgreSQL but a text column in SQLite, and the two
// drivers hand the value back differently: a PG date scanned into a Go string
// arrives as the RFC3339 rendering ("2026-09-10T00:00:00Z"), while SQLite returns
// exactly what was written ("2026-09-10"). Callers parse the day and want the day
// *as stored* — never converted into another zone — so the trimming happens here
// instead of in every caller.
//
// This is not cosmetic. Before it existed, the aggregation's date parse failed on
// every PostgreSQL bucket, and a failed parse skips the bucket: neighbour warmth
// silently went to zero (it has no lumped fallback at all) while view slicing
// quietly fell back to the lumped row — on SQLite everything looked green.
func normalizeBucketDay(raw string) string {
	if len(raw) > 10 && (raw[10] == 'T' || raw[10] == ' ') {
		return raw[:10]
	}
	return raw
}

// ListDailyViews returns the view buckets of one KB, split at cutoff. See
// listDailyBuckets for the split's shape and why the fold is exact.
func (r *masteryRepository) ListDailyViews(
	ctx context.Context, scope interfaces.MemoryScope, kbID string, cutoff time.Time,
) (recent, cold []types.MasteryDailyBucket, err error) {
	return r.listDailyBuckets(ctx, scope, kbID, viewDailyLedger, cutoff)
}

// spreadWriteBatchSize bounds how many neighbour rows go into one statement. It is
// a safety valve for pathological pages, not the normal path: pages in the dev KB
// average ~8 links and peak just under 60, i.e. one statement.
const spreadWriteBatchSize = 128

// BumpSpreadViews credits the pages adjacent to a viewed page with the reader's
// time on it.
//
// Only time spreads: the caller passes the clamped view duration, so a
// few-second glance credits a few seconds (worth nothing at scoring time) while a
// real read warms its neighbours a little. Self is skipped, and duplicates are
// collapsed so a page that is both an in-link and an out-link is credited once.
//
// All neighbours go in one multi-row upsert. This runs on the page-view request
// path, and a page links dozens of neighbours: written per row, one effective view
// cost as many round trips as the page has links (and a failure mid-loop left half
// the neighbours credited, with no transaction to roll it back). The deduplication
// above is load-bearing for that batching: PostgreSQL rejects a statement that
// touches one conflict key twice ("ON CONFLICT DO UPDATE command cannot affect row
// a second time"), so the collapse has to happen in Go before the insert.
func (r *masteryRepository) BumpSpreadViews(
	ctx context.Context, scope interfaces.MemoryScope, kbID, sourceSlug string, neighbors []string, seconds int64,
) error {
	if kbID == "" || sourceSlug == "" || seconds <= 0 || len(neighbors) == 0 {
		return nil
	}
	now := time.Now()
	date := now.Format("2006-01-02")
	rows := make([]*types.MemorySpreadView, 0, len(neighbors))
	seen := make(map[string]struct{}, len(neighbors))
	for _, slug := range neighbors {
		if slug == "" || slug == sourceSlug {
			continue
		}
		if _, dup := seen[slug]; dup {
			continue
		}
		seen[slug] = struct{}{}
		rows = append(rows, &types.MemorySpreadView{
			ID:              uuid.New().String(),
			TenantID:        scope.TenantID,
			SubjectID:       scope.SubjectID,
			KnowledgeBaseID: kbID,
			Slug:            slug,
			EventDate:       date,
			SpreadSeconds:   seconds,
		})
	}
	if len(rows) == 0 {
		return nil
	}
	// spread_seconds is qualified by table and by EXCLUDED for a reason: inside
	// ON CONFLICT DO UPDATE, a bare column name is ambiguous between the target
	// row and the proposed one, and PostgreSQL refuses it outright.
	return r.db.WithContext(ctx).
		Clauses(clause.OnConflict{
			Columns: []clause.Column{
				{Name: "tenant_id"}, {Name: "subject_id"}, {Name: "knowledge_base_id"},
				{Name: "slug"}, {Name: "event_date"},
			},
			DoUpdates: clause.Assignments(map[string]interface{}{
				"spread_seconds": gorm.Expr("memory_spread_views.spread_seconds + excluded.spread_seconds"),
				"updated_at":     now,
			}),
		}).
		CreateInBatches(rows, spreadWriteBatchSize).Error
}

// ListDailySpread returns the neighbour-spread buckets of one KB, split at cutoff
// under the same rule as ListDailyViews.
func (r *masteryRepository) ListDailySpread(
	ctx context.Context, scope interfaces.MemoryScope, kbID string, cutoff time.Time,
) (recent, cold []types.MasteryDailyBucket, err error) {
	return r.listDailyBuckets(ctx, scope, kbID, spreadDailyLedger, cutoff)
}

func (r *masteryRepository) ListActiveLikes(
	ctx context.Context, scope interfaces.MemoryScope,
) ([]*types.MemoryAnswerLike, error) {
	var rows []*types.MemoryAnswerLike
	err := r.scoped(ctx, scope).
		Where("cancelled_at IS NULL").
		Find(&rows).Error
	return rows, err
}

// DeleteAll drops every guidance ledger row in the scope. It deliberately does
// not touch memory_doc_affinity or any long-term memory table.
func (r *masteryRepository) DeleteAll(ctx context.Context, scope interfaces.MemoryScope) error {
	for _, model := range []interface{}{
		&types.MemorySpreadView{},
		&types.MemoryMasteryDaily{},
		&types.MemoryGuideExposure{},
		&types.MemoryAnswerLike{},
		&types.MemoryPageView{},
		&types.MemoryCitation{},
	} {
		if err := r.scoped(ctx, scope).Delete(model).Error; err != nil {
			return err
		}
	}
	return nil
}
