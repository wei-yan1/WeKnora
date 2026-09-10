package mastery

import (
	"context"
	"sort"
	"time"

	"github.com/Tencent/WeKnora/internal/application/service/memory"
	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// Service implements interfaces.MasteryService. It resolves the memory scope
// from the request context (never from a client id) and delegates storage to the
// ledger repository, keeping the water-level computation a pure function.
type Service struct {
	repo   interfaces.MasteryRepository
	config Config
}

// NewMasteryService creates the knowledge-guidance service.
func NewMasteryService(repo interfaces.MasteryRepository) interfaces.MasteryService {
	return &Service{repo: repo, config: DefaultConfig()}
}

func (s *Service) RecordCitations(ctx context.Context, refs []types.MemoryDocAffinity) {
	if len(refs) == 0 {
		return
	}
	scope, err := memory.ResolveScope(ctx)
	if err != nil {
		return
	}
	if err := s.repo.BumpCitation(ctx, scope, refs); err != nil {
		logger.Warnf(ctx, "mastery: record citations failed: %v", err)
	}
}

func (s *Service) RecordPageView(ctx context.Context, kbID, slug string, duration int64, neighbors []string) {
	if slug == "" || duration <= 0 {
		return
	}
	// 服务端也钳制单次时长，防止绕过 handler 的调用路径刷高水位。
	if duration > types.MasteryMaxPageViewSeconds {
		duration = types.MasteryMaxPageViewSeconds
	}
	scope, err := memory.ResolveScope(ctx)
	if err != nil {
		return
	}
	if err := s.repo.BumpPageView(ctx, scope, kbID, slug, duration); err != nil {
		logger.Warnf(ctx, "mastery: record page view failed: %v", err)
		return
	}
	// 一跳邻居的「上下文预热」：按本次阅读时间折算，扫一眼几乎不产生分数。
	// 放在浏览写入成功之后，避免主证据写失败却留下扩散痕迹。
	if err := s.repo.BumpSpreadViews(ctx, scope, kbID, slug, neighbors, duration); err != nil {
		logger.Warnf(ctx, "mastery: record neighbour spread failed: %v", err)
	}
	// 有效浏览既是状态信号，也是曝光回报信号：若该 slug 曾作为边界候选被
	// 展示/点击，则回填 qualified_view_at，闭环「展示→点击→有效浏览」。
	if err := s.repo.MarkExposureQualified(ctx, scope, kbID, slug); err != nil {
		logger.Warnf(ctx, "mastery: mark exposure qualified failed: %v", err)
	}
}

func (s *Service) RecordAnswerLike(ctx context.Context, messageID string, refs []types.MemoryDocAffinity) {
	if messageID == "" {
		return
	}
	allocations := AllocateLikes(refs)
	if len(allocations) == 0 {
		return
	}
	scope, err := memory.ResolveScope(ctx)
	if err != nil {
		return
	}
	like := &types.MemoryAnswerLike{MessageID: messageID, Allocations: allocations}
	if err := s.repo.RecordAnswerLike(ctx, scope, like); err != nil {
		logger.Warnf(ctx, "mastery: record answer like failed: %v", err)
	}
}

func (s *Service) CancelAnswerLike(ctx context.Context, messageID string) {
	scope, err := memory.ResolveScope(ctx)
	if err != nil {
		return
	}
	if err := s.repo.CancelAnswerLike(ctx, scope, messageID); err != nil {
		logger.Warnf(ctx, "mastery: cancel answer like failed: %v", err)
	}
}

func (s *Service) RecordExposure(ctx context.Context, exp *types.MemoryGuideExposure) {
	if exp == nil {
		return
	}
	scope, err := memory.ResolveScope(ctx)
	if err != nil {
		return
	}
	if err := s.repo.RecordExposure(ctx, scope, exp); err != nil {
		logger.Warnf(ctx, "mastery: record exposure failed: %v", err)
	}
}

// aggregate loads the ledgers and projects them onto pages, returning the
// evidence breakdown per slug. Citation and like evidence are recorded by
// knowledge id, so they are projected onto pages via slugSources (from
// WikiPage.SourceRefs); view evidence is recorded directly by slug.
func (s *Service) aggregate(
	ctx context.Context, kbID string, slugSources map[string][]string,
) (map[string]Evidence, error) {
	scope, err := memory.ResolveScope(ctx)
	if err != nil {
		return map[string]Evidence{}, nil
	}
	// 没有节点可投影时，证据一定为空：先返回，省掉四次账本查询。
	if len(slugSources) == 0 {
		return map[string]Evidence{}, nil
	}

	citations, err := s.repo.ListCitations(ctx, scope, kbID)
	if err != nil {
		return nil, err
	}
	views, err := s.repo.ListPageViews(ctx, scope, kbID)
	if err != nil {
		return nil, err
	}
	likes, err := s.repo.ListActiveLikes(ctx, scope)
	if err != nil {
		return nil, err
	}
	// 冷桶折叠：早于衰减地平线的日桶，每条都已经按 DecayFloor 衰减，因此可以按
	// slug 合并成一条；地平线之内仍逐日保留，各付各的衰减。配置退化（无下限）
	// 时地平线为 0，此时把 cutoff 放在纪元起点，等价于「全部逐日保留」。
	now := time.Now()
	cutoff := time.Unix(0, 0)
	if horizon := s.config.ColdHorizon(); horizon > 0 {
		cutoff = now.Add(-horizon)
	}
	recentViews, coldViews, err := s.repo.ListDailyViews(ctx, scope, kbID, cutoff)
	if err != nil {
		return nil, err
	}
	recentSpread, coldSpread, err := s.repo.ListDailySpread(ctx, scope, kbID, cutoff)
	if err != nil {
		return nil, err
	}

	citeByDoc := make(map[string]*types.MemoryCitation, len(citations))
	for _, c := range citations {
		citeByDoc[c.KnowledgeID] = c
	}

	type likeAgg struct {
		weight float64
		last   time.Time
	}
	likeByDoc := make(map[string]*likeAgg)
	for _, l := range likes {
		for _, alloc := range l.Allocations {
			agg := likeByDoc[alloc.KnowledgeID]
			if agg == nil {
				agg = &likeAgg{}
				likeByDoc[alloc.KnowledgeID] = agg
			}
			agg.weight += alloc.CreditedWeight
			if l.LikedAt.After(agg.last) {
				agg.last = l.LikedAt
			}
		}
	}

	viewBySlug := make(map[string]*types.MemoryPageView, len(views))
	for _, v := range views {
		viewBySlug[v.Slug] = v
	}

	// view 证据按天切片，供 Level 逐日衰减（理由见 ViewSlice 的类型注释）。
	// 日桶由 BumpPageView 在每次浏览时写入，次数与时长与 memory_page_views 同口径。
	viewSlices := make(map[string][]ViewSlice)
	spreadSlices := make(map[string][]SpreadSlice)
	appendView := func(b types.MasteryDailyBucket, day time.Time) {
		viewSlices[b.Slug] = append(viewSlices[b.Slug], ViewSlice{
			Day:      day,
			Views:    b.Count,
			Duration: b.Seconds,
		})
	}
	appendSpread := func(b types.MasteryDailyBucket, day time.Time) {
		spreadSlices[b.Slug] = append(spreadSlices[b.Slug], SpreadSlice{Day: day, Seconds: b.Seconds})
	}
	// 唯一的日期解析点，浏览与预热共用：近端桶自带日期；折叠桶（EventDate 为空）
	// 取 cutoff 当天作代表——桶内每一天的衰减因子都已经是 DecayFloor，与逐日展开
	// 完全等价。单个坏桶只跳过，不拖垮整张图的水位计算；坏桶数在四次遍历后汇总成
	// 一条告警——逐桶刷 warning 会把日志淹掉，而它恰恰是「日桶格式变了」的信号。
	badDates := 0
	firstBadDate := ""
	forEachBucket := func(
		buckets []types.MasteryDailyBucket, onEach func(b types.MasteryDailyBucket, day time.Time),
	) {
		for _, b := range buckets {
			day := cutoff
			if b.EventDate != "" {
				parsed, perr := time.ParseInLocation("2006-01-02", b.EventDate, time.Local)
				if perr != nil {
					badDates++
					if firstBadDate == "" {
						firstBadDate = b.EventDate
					}
					continue
				}
				day = parsed
			}
			onEach(b, day)
		}
	}
	forEachBucket(recentViews, appendView)
	forEachBucket(coldViews, appendView)
	forEachBucket(recentSpread, appendSpread)
	forEachBucket(coldSpread, appendSpread)
	if badDates > 0 {
		// 这不是「无关紧要的脏数据」：跳过整桶会让预热（无汇总兜底）静默归零、
		// 让浏览切片悄悄退回汇总行。仓储层负责把方言渲染归一成 YYYY-MM-DD。
		logger.Warnf(ctx, "mastery: skipped %d daily bucket(s) whose date could not be parsed "+
			"(first %q); bucket dates must arrive normalized to YYYY-MM-DD", badDates, firstBadDate)
	}

	result := make(map[string]Evidence, len(slugSources))
	for slug, docs := range slugSources {
		e := Evidence{}
		// Citation: take the strongest source doc. Summary pages map 1:1 to a
		// document; a multi-source page (entity/concept) inherits the strongest
		// source's citation strength, which keeps the projection conservative —
		// one heavily cited source never inflates a page the user barely touched.
		for _, d := range docs {
			if c, ok := citeByDoc[d]; ok {
				if c.CiteCount > e.Citations {
					e.Citations = c.CiteCount
				}
				if c.LastCitedAt.After(e.LastCitedAt) {
					e.LastCitedAt = c.LastCitedAt
				}
			}
			if agg, ok := likeByDoc[d]; ok {
				e.Likes += agg.weight
				if agg.last.After(e.LastLikedAt) {
					e.LastLikedAt = agg.last
				}
			}
		}
		if v, ok := viewBySlug[slug]; ok {
			e.Views = v.ViewCount
			e.Duration = v.TotalDuration
			e.LastViewedAt = v.LastViewAt
		}
		// nil 时 Level 回落到汇总口径（历史数据 / 无日桶的库）。
		e.ViewSlices = viewSlices[slug]
		e.SpreadSlices = spreadSlices[slug]
		result[slug] = e
	}
	return result, nil
}

// NodeMastery computes the water level per slug.
func (s *Service) NodeMastery(
	ctx context.Context, kbID string, slugSources map[string][]string,
) (map[string]int, error) {
	states, err := s.NodeStates(ctx, kbID, slugSources)
	if err != nil {
		return nil, err
	}
	result := make(map[string]int, len(states))
	for slug, st := range states {
		result[slug] = st.Level
	}
	return result, nil
}

// NodeStates computes the full per-node state: water level plus last-active
// time and a server-computed recency flag. RecentlyActive uses the freshness
// window (visual-only), independent of the half-life decay that drives the
// water level itself.
func (s *Service) NodeStates(
	ctx context.Context, kbID string, slugSources map[string][]string,
) (map[string]types.MasteryNodeState, error) {
	evidence, err := s.aggregate(ctx, kbID, slugSources)
	if err != nil {
		return nil, err
	}
	return s.statesFromEvidence(evidence, time.Now()), nil
}

// NodeStateForSlug returns the state of a single page, reusing the same
// aggregation as NodeStates so the value echoed back after a page view matches
// the graph overlay exactly.
func (s *Service) NodeStateForSlug(
	ctx context.Context, kbID, slug string, sourceKnowledgeIDs []string,
) (types.MasteryNodeState, error) {
	states, err := s.NodeStates(ctx, kbID, map[string][]string{slug: sourceKnowledgeIDs})
	if err != nil {
		return types.MasteryNodeState{}, err
	}
	if st, ok := states[slug]; ok {
		return st, nil
	}
	return types.MasteryNodeState{Level: 0}, nil
}

func (s *Service) statesFromEvidence(
	evidence map[string]Evidence, now time.Time,
) map[string]types.MasteryNodeState {
	freshWindow := time.Duration(s.config.FreshnessDays*24) * time.Hour
	result := make(map[string]types.MasteryNodeState, len(evidence))
	for slug, e := range evidence {
		lastActive := e.LastActive()
		recent := !lastActive.IsZero() && now.Sub(lastActive) <= freshWindow
		result[slug] = types.MasteryNodeState{
			Level:          Level(s.config, e, now),
			LastActive:     lastActive,
			RecentlyActive: recent,
		}
	}
	return result
}

// Profile returns the evidence breakdown per node, sorted by level desc, so a
// user can inspect exactly which signals formed each node's water level.
func (s *Service) Profile(
	ctx context.Context, kbID string, slugSources map[string][]string,
) ([]types.MasteryNodeDetail, error) {
	evidence, err := s.aggregate(ctx, kbID, slugSources)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	out := make([]types.MasteryNodeDetail, 0, len(evidence))
	for slug, e := range evidence {
		level := Level(s.config, e, now)
		out = append(out, types.MasteryNodeDetail{
			Slug:       slug,
			Level:      level,
			Tier:       types.MasteryTierLabel(level),
			Citations:  e.Citations,
			Views:      e.Views,
			Duration:   e.Duration,
			Likes:      e.Likes,
			Spread:     e.decayedSpreadScore(s.config, now),
			LastActive: e.LastActive(),
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Level != out[j].Level {
			return out[i].Level > out[j].Level
		}
		return out[i].Slug < out[j].Slug
	})
	return out, nil
}

// RecordExposures records the local ripple candidates shown when the user
// clicks a center node. Each candidate carries rank = index + 1 so the
// click-through evaluation can answer "which position did the user follow".
// Deduplicated per (kb, trigger, candidate, day).
func (s *Service) RecordExposures(ctx context.Context, kbID, triggerSlug string, candidates []string) {
	if kbID == "" || len(candidates) == 0 {
		return
	}
	scope, err := memory.ResolveScope(ctx)
	if err != nil {
		return
	}
	for i, slug := range candidates {
		if slug == "" {
			continue
		}
		dup, err := s.repo.HasExposureToday(ctx, scope, kbID, triggerSlug, slug)
		if err != nil {
			logger.Warnf(ctx, "mastery: check exposure dup failed: %v", err)
			continue
		}
		if dup {
			continue
		}
		if err := s.repo.RecordExposure(ctx, scope, &types.MemoryGuideExposure{
			KnowledgeBaseID: kbID,
			TriggerSlug:     triggerSlug,
			CandidateSlug:   slug,
			Strategy:        types.GuideStrategyPPRBoundary,
			Rank:            i + 1,
			ShownAt:         time.Now(),
		}); err != nil {
			logger.Warnf(ctx, "mastery: record exposure failed: %v", err)
		}
	}
}

// MarkExposureClicked marks an exposure as clicked (the candidate the user
// actually followed up on).
func (s *Service) MarkExposureClicked(ctx context.Context, kbID, candidateSlug string) {
	if kbID == "" || candidateSlug == "" {
		return
	}
	scope, err := memory.ResolveScope(ctx)
	if err != nil {
		return
	}
	if err := s.repo.MarkExposureClicked(ctx, scope, kbID, candidateSlug); err != nil {
		logger.Warnf(ctx, "mastery: mark exposure clicked failed: %v", err)
	}
}

// MarkExposureQualified marks an exposure as qualified (the candidate page was
// effectively viewed). It is also invoked from RecordPageView, so a qualifying
// view of a previously shown boundary candidate closes the evaluation loop.
func (s *Service) MarkExposureQualified(ctx context.Context, kbID, slug string) {
	if kbID == "" || slug == "" {
		return
	}
	scope, err := memory.ResolveScope(ctx)
	if err != nil {
		return
	}
	if err := s.repo.MarkExposureQualified(ctx, scope, kbID, slug); err != nil {
		logger.Warnf(ctx, "mastery: mark exposure qualified failed: %v", err)
	}
}

// Boundary exposes the PPR frontier as a slug → score map for the graph
// endpoint. A non-zero score means the slug is on the knowledge boundary; the
// score itself lets the frontend rank one-hop neighbors when producing the
// "next step" recommendations.
func (s *Service) Boundary(levels map[string]int, adjacency map[string][]string) map[string]float64 {
	nodes := ComputeBoundary(levels, adjacency, FamiliarLevel, DefaultBoundaryTopK)
	result := make(map[string]float64, len(nodes))
	for _, n := range nodes {
		result[n.Slug] = n.Score
	}
	return result
}

func (s *Service) DeleteAll(ctx context.Context) error {
	scope, err := memory.ResolveScope(ctx)
	if err != nil {
		return err
	}
	return s.repo.DeleteAll(ctx, scope)
}
