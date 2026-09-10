package interfaces

import (
	"context"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
)

// MasteryRepository is the storage contract for the knowledge-guidance ledgers.
// Like MemoryRepository, every method takes an explicit scope rather than
// reading it from ctx, and all rows are scoped by (tenant_id, subject_id).
type MasteryRepository interface {
	// BumpCitation records that an answer cited documents, in the guidance
	// citation ledger (decoupled from memory_doc_affinity).
	BumpCitation(ctx context.Context, scope MemoryScope, docs []types.MemoryDocAffinity) error
	// BumpPageView records one effective page view with its duration.
	BumpPageView(ctx context.Context, scope MemoryScope, kbID, slug string, duration int64) error
	// RecordAnswerLike upserts a like bound to a message, storing its
	// allocation snapshot.
	RecordAnswerLike(ctx context.Context, scope MemoryScope, like *types.MemoryAnswerLike) error
	// CancelAnswerLike marks a like cancelled (its allocations stop counting).
	CancelAnswerLike(ctx context.Context, scope MemoryScope, messageID string) error
	// RecordExposure writes one guidance exposure row.
	RecordExposure(ctx context.Context, scope MemoryScope, exp *types.MemoryGuideExposure) error
	// ListCitations returns all citation rows for a KB.
	ListCitations(ctx context.Context, scope MemoryScope, kbID string) ([]*types.MemoryCitation, error)
	// ListPageViews returns all page-view rows for a KB.
	ListPageViews(ctx context.Context, scope MemoryScope, kbID string) ([]*types.MemoryPageView, error)
	// BumpSpreadViews credits the pages adjacent to a viewed page with the reader's
	// time on it (neighbour warmth). Self is skipped and duplicates collapse.
	BumpSpreadViews(ctx context.Context, scope MemoryScope, kbID, sourceSlug string, neighbors []string, seconds int64) error
	// ListDailySpread returns the neighbour-spread buckets of one KB split at
	// cutoff, in the same shape and under the same folding rule as ListDailyViews.
	ListDailySpread(ctx context.Context, scope MemoryScope, kbID string, cutoff time.Time) (recent, cold []types.MasteryDailyBucket, err error)
	// ListDailyViews returns the view buckets of one KB split at cutoff: days on
	// or after it as one row per (slug, day), older days as one summed row per
	// slug. The buckets are the time-sliced form of the view evidence, letting the
	// water level decay each day on its own clock instead of decaying a lumped
	// total by a single latest timestamp; the split keeps that read bounded.
	ListDailyViews(ctx context.Context, scope MemoryScope, kbID string, cutoff time.Time) (recent, cold []types.MasteryDailyBucket, err error)
	// ListActiveLikes returns all non-cancelled like rows.
	ListActiveLikes(ctx context.Context, scope MemoryScope) ([]*types.MemoryAnswerLike, error)
	// HasExposureToday reports whether the scope already exposed (trigger,
	// candidate) in the given KB today, so repeated clicks on the same center
	// don't spam the exposure log.
	HasExposureToday(ctx context.Context, scope MemoryScope, kbID, triggerSlug, candidateSlug string) (bool, error)
	// MarkExposureClicked marks the most recent unclicked exposure for a
	// candidate in a KB as clicked.
	MarkExposureClicked(ctx context.Context, scope MemoryScope, kbID, candidateSlug string) error
	// MarkExposureQualified marks the most recent unqualified exposure for a
	// candidate in a KB as qualified (the reward signal: the page was actually
	// viewed past the effective-view threshold).
	MarkExposureQualified(ctx context.Context, scope MemoryScope, kbID, slug string) error
	// DeleteAll drops every guidance ledger row in the scope.
	DeleteAll(ctx context.Context, scope MemoryScope) error
}

// MasteryService computes the personal knowledge state from the ledgers and is
// the write/read API used by handlers and the graph endpoint.
type MasteryService interface {
	// RecordCitations records cited docs, called alongside the existing answer
	// source recording so the guidance ledger stays in sync without touching
	// memory_doc_affinity.
	RecordCitations(ctx context.Context, refs []types.MemoryDocAffinity)
	// RecordPageView records an effective page view. neighbors are the slugs linked
	// to and from the viewed page (one hop, deduplicated by the caller); a fraction
	// of the reading time is credited to them as neighbour warmth.
	RecordPageView(ctx context.Context, kbID, slug string, duration int64, neighbors []string)
	// RecordAnswerLike records a like for an answer message, splitting the
	// sub-linearly capped gain across the cited knowledge ids (with their KB
	// ids) into an allocation snapshot.
	RecordAnswerLike(ctx context.Context, messageID string, refs []types.MemoryDocAffinity)
	// CancelAnswerLike revokes a like.
	CancelAnswerLike(ctx context.Context, messageID string)
	// RecordExposure records a guidance exposure.
	RecordExposure(ctx context.Context, exp *types.MemoryGuideExposure)
	// NodeMastery returns the water level (0..100) per slug for a KB.
	// slugSources maps slug → its source knowledge ids (from WikiPage.SourceRefs),
	// so citation and like evidence (recorded by knowledge id) can be projected
	// onto pages; view evidence is recorded directly by slug.
	NodeMastery(ctx context.Context, kbID string, slugSources map[string][]string) (map[string]int, error)
	// NodeStates returns the full per-node state: water level plus last-active
	// time and a server-computed recency flag. The graph overlay uses level for
	// water height and recently_active for the ripple animation, so the client
	// never has to do its own clock math.
	NodeStates(ctx context.Context, kbID string, slugSources map[string][]string) (map[string]types.MasteryNodeState, error)
	// NodeStateForSlug returns the state of a single page. slugSources carries
	// just that slug's source knowledge ids; the implementation reuses the same
	// aggregation as NodeStates so the returned value matches the graph overlay
	// exactly. Used by the page-view endpoint to echo back the fresh level.
	NodeStateForSlug(ctx context.Context, kbID, slug string, sourceKnowledgeIDs []string) (types.MasteryNodeState, error)
	// Boundary returns the knowledge frontier (top boundary slugs) computed via
	// personalized PageRank seeded from familiar nodes, mapped to their PPR
	// score so callers can rank candidates.
	Boundary(levels map[string]int, adjacency map[string][]string) map[string]float64
	// RecordExposures records the local ripple candidates shown when the user
	// clicks a center node (deduplicated per day), feeding the PPR
	// click-through evaluation. candidates carry rank = index + 1.
	RecordExposures(ctx context.Context, kbID, triggerSlug string, candidates []string)
	// MarkExposureClicked marks an exposure as clicked (the candidate the user
	// actually followed up on).
	MarkExposureClicked(ctx context.Context, kbID, candidateSlug string)
	// MarkExposureQualified marks an exposure as qualified (the candidate page
	// was effectively viewed).
	MarkExposureQualified(ctx context.Context, kbID, slug string)
	// Profile returns the evidence breakdown per node, so the user can inspect
	// exactly which signals formed each node's water level.
	Profile(ctx context.Context, kbID string, slugSources map[string][]string) ([]types.MasteryNodeDetail, error)
	// DeleteAll clears the person's guidance ledgers.
	DeleteAll(ctx context.Context) error
}
