package usagecompat

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	internallogging "github.com/router-for-me/CLIProxyAPI/v7/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/redisqueue"
	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
	log "github.com/sirupsen/logrus"
)

func init() {
	coreusage.RegisterPlugin(NewLoggerPlugin())
}

// LoggerPlugin collects in-memory request statistics for the legacy usage endpoints.
type LoggerPlugin struct {
	stats *RequestStatistics
}

// NewLoggerPlugin constructs a logger plugin wired to the shared compatibility statistics store.
func NewLoggerPlugin() *LoggerPlugin { return &LoggerPlugin{stats: defaultRequestStatistics} }

// HandleUsage updates the in-memory compatibility statistics store for each usage record.
func (p *LoggerPlugin) HandleUsage(ctx context.Context, record coreusage.Record) {
	if p == nil || p.stats == nil {
		return
	}
	if !redisqueue.UsageStatisticsEnabled() {
		return
	}
	p.stats.Record(ctx, record)
}

// RequestStatistics maintains aggregated request metrics in memory.
type RequestStatistics struct {
	mu sync.RWMutex

	totalRequests int64
	successCount  int64
	failureCount  int64
	totalTokens   int64

	apis map[string]*apiStats

	requestsByDay  map[string]int64
	requestsByHour map[int]int64
	tokensByDay    map[string]int64
	tokensByHour   map[int]int64

	detailStore *sqliteDetailStore
}

type apiStats struct {
	TotalRequests int64
	TotalTokens   int64
	Models        map[string]*modelStats
}

type modelStats struct {
	TotalRequests int64
	TotalTokens   int64
	Details       []RequestDetail
}

// RequestDetail stores the timestamp, latency, and token usage for a single request.
type RequestDetail struct {
	Timestamp       time.Time  `json:"timestamp"`
	LatencyMs       int64      `json:"latency_ms"`
	Source          string     `json:"source"`
	AuthIndex       string     `json:"auth_index"`
	ReasoningEffort string     `json:"reasoning_effort,omitempty"`
	Tokens          TokenStats `json:"tokens"`
	Failed          bool       `json:"failed"`
}

// TokenStats captures the token usage breakdown for a request.
type TokenStats struct {
	InputTokens     int64 `json:"input_tokens"`
	OutputTokens    int64 `json:"output_tokens"`
	ReasoningTokens int64 `json:"reasoning_tokens"`
	CachedTokens    int64 `json:"cached_tokens"`
	TotalTokens     int64 `json:"total_tokens"`
}

// StatisticsSnapshot represents an immutable view of the aggregated metrics.
type StatisticsSnapshot struct {
	TotalRequests int64 `json:"total_requests"`
	SuccessCount  int64 `json:"success_count"`
	FailureCount  int64 `json:"failure_count"`
	TotalTokens   int64 `json:"total_tokens"`

	APIs map[string]APISnapshot `json:"apis"`

	RequestsByDay  map[string]int64 `json:"requests_by_day"`
	RequestsByHour map[string]int64 `json:"requests_by_hour"`
	TokensByDay    map[string]int64 `json:"tokens_by_day"`
	TokensByHour   map[string]int64 `json:"tokens_by_hour"`
}

// APISnapshot summarises metrics for a single API identifier.
type APISnapshot struct {
	TotalRequests int64                    `json:"total_requests"`
	TotalTokens   int64                    `json:"total_tokens"`
	Models        map[string]ModelSnapshot `json:"models"`
}

// ModelSnapshot summarises metrics for a specific model.
type ModelSnapshot struct {
	TotalRequests    int64           `json:"total_requests"`
	TotalTokens      int64           `json:"total_tokens"`
	Details          []RequestDetail `json:"details"`
	DetailsTruncated bool            `json:"details_truncated,omitempty"`
	DetailsLimit     int             `json:"details_limit,omitempty"`
}

// MergeResult reports how many imported request details were added or skipped.
type MergeResult struct {
	Added   int64 `json:"added"`
	Skipped int64 `json:"skipped"`
}

// UsageDetailRow is a paginated request detail with its API and model identifiers.
type UsageDetailRow struct {
	ID              int64      `json:"id"`
	API             string     `json:"api"`
	Model           string     `json:"model"`
	Timestamp       time.Time  `json:"timestamp"`
	LatencyMs       int64      `json:"latency_ms"`
	Source          string     `json:"source"`
	AuthIndex       string     `json:"auth_index"`
	ReasoningEffort string     `json:"reasoning_effort,omitempty"`
	Tokens          TokenStats `json:"tokens"`
	Failed          bool       `json:"failed"`
}

// DetailPageQuery describes a paginated detail lookup.
type DetailPageQuery struct {
	API       string
	Model     string
	Source    string
	AuthIndex string
	Search    string
	Page      int
	PageSize  int
	Offset    int
}

// DetailPage contains paginated request details.
type DetailPage struct {
	Items    []UsageDetailRow `json:"items"`
	Total    int64            `json:"total"`
	Page     int              `json:"page"`
	PageSize int              `json:"page_size"`
	Offset   int              `json:"offset"`
	HasMore  bool             `json:"has_more"`
}

// AggregateQuery describes a compact aggregate lookup.
type AggregateQuery struct {
	Range string
	Since time.Time
	Until time.Time
}

// AggregateSnapshot contains compact usage statistics without request details.
type AggregateSnapshot struct {
	TotalRequests int64      `json:"total_requests"`
	SuccessCount  int64      `json:"success_count"`
	FailureCount  int64      `json:"failure_count"`
	TotalTokens   int64      `json:"total_tokens"`
	Tokens        TokenStats `json:"tokens"`

	APIs   map[string]AggregateAPI   `json:"apis"`
	Models map[string]AggregateModel `json:"models"`
	Hourly []AggregateBucket         `json:"hourly"`
	Daily  []AggregateBucket         `json:"daily"`
	Range  string                    `json:"range"`
	Since  *time.Time                `json:"since,omitempty"`
	Until  *time.Time                `json:"until,omitempty"`
}

// AggregateAPI contains compact statistics for an API identifier.
type AggregateAPI struct {
	TotalRequests int64                     `json:"total_requests"`
	SuccessCount  int64                     `json:"success_count"`
	FailureCount  int64                     `json:"failure_count"`
	TotalTokens   int64                     `json:"total_tokens"`
	Tokens        TokenStats                `json:"tokens"`
	Models        map[string]AggregateModel `json:"models"`
}

// AggregateModel contains compact statistics for a model.
type AggregateModel struct {
	TotalRequests int64      `json:"total_requests"`
	SuccessCount  int64      `json:"success_count"`
	FailureCount  int64      `json:"failure_count"`
	TotalTokens   int64      `json:"total_tokens"`
	Tokens        TokenStats `json:"tokens"`
}

// AggregateBucket contains compact token usage for a model in a time bucket.
type AggregateBucket struct {
	Bucket        time.Time  `json:"bucket"`
	API           string     `json:"api"`
	Model         string     `json:"model"`
	TotalRequests int64      `json:"total_requests"`
	SuccessCount  int64      `json:"success_count"`
	FailureCount  int64      `json:"failure_count"`
	TotalTokens   int64      `json:"total_tokens"`
	Tokens        TokenStats `json:"tokens"`
}

// SnapshotOptions controls how much detail data a statistics snapshot includes.
type SnapshotOptions struct {
	DetailLimit int
}

const (
	SnapshotAllDetails       = -1
	defaultSnapshotDetailCap = SnapshotAllDetails
	inMemoryDetailCacheLimit = 512
)

var defaultRequestStatistics = NewRequestStatistics()

// DefaultStatistics returns the shared compatibility statistics store.
func DefaultStatistics() *RequestStatistics { return defaultRequestStatistics }

// NewRequestStatistics constructs an empty statistics store.
func NewRequestStatistics() *RequestStatistics {
	stats := &RequestStatistics{
		apis:           make(map[string]*apiStats),
		requestsByDay:  make(map[string]int64),
		requestsByHour: make(map[int]int64),
		tokensByDay:    make(map[string]int64),
		tokensByHour:   make(map[int]int64),
	}
	store, err := newSQLiteDetailStore(defaultSQLiteDetailStorePath())
	if err != nil {
		log.Warnf("usagecompat: sqlite detail store unavailable: %v", err)
		return stats
	}
	stats.detailStore = store
	if err := stats.loadDetailsFromStore(); err != nil {
		log.Warnf("usagecompat: failed to load sqlite usage details: %v", err)
	}
	return stats
}

// Record ingests a new usage record and updates the aggregates.
func (s *RequestStatistics) Record(ctx context.Context, record coreusage.Record) {
	if s == nil {
		return
	}
	if !redisqueue.UsageStatisticsEnabled() {
		return
	}

	timestamp := record.RequestedAt
	if timestamp.IsZero() {
		timestamp = time.Now()
	}
	detail := normalizeDetail(record.Detail)
	totalTokens := detail.TotalTokens
	statsKey := resolveAPIIdentifier(ctx, record)
	failed := record.Failed
	if !failed {
		failed = !resolveSuccess(ctx)
	}
	success := !failed
	modelName := strings.TrimSpace(record.Model)
	if modelName == "" {
		modelName = "unknown"
	}
	dayKey := timestamp.Format("2006-01-02")
	hourKey := timestamp.Hour()

	requestDetail := RequestDetail{
		Timestamp:       timestamp,
		LatencyMs:       normalizeLatency(record.Latency),
		Source:          record.Source,
		AuthIndex:       record.AuthIndex,
		ReasoningEffort: strings.TrimSpace(record.ReasoningEffort),
		Tokens:          detail,
		Failed:          failed,
	}

	s.mu.Lock()
	s.totalRequests++
	if success {
		s.successCount++
	} else {
		s.failureCount++
	}
	s.totalTokens += totalTokens

	stats, ok := s.apis[statsKey]
	if !ok {
		stats = &apiStats{Models: make(map[string]*modelStats)}
		s.apis[statsKey] = stats
	}
	s.updateAPIStats(stats, modelName, requestDetail)

	s.requestsByDay[dayKey]++
	s.requestsByHour[hourKey]++
	s.tokensByDay[dayKey] += totalTokens
	s.tokensByHour[hourKey] += totalTokens
	s.mu.Unlock()

	if s.detailStore != nil {
		if _, err := s.detailStore.Insert(statsKey, modelName, requestDetail); err != nil {
			log.Warnf("usagecompat: failed to persist usage detail: %v", err)
		}
	}
}

func (s *RequestStatistics) updateAPIStats(stats *apiStats, model string, detail RequestDetail) {
	stats.TotalRequests++
	stats.TotalTokens += detail.Tokens.TotalTokens
	modelStatsValue, ok := stats.Models[model]
	if !ok {
		modelStatsValue = &modelStats{}
		stats.Models[model] = modelStatsValue
	}
	modelStatsValue.TotalRequests++
	modelStatsValue.TotalTokens += detail.Tokens.TotalTokens
	modelStatsValue.Details = append(modelStatsValue.Details, detail)
	if len(modelStatsValue.Details) > inMemoryDetailCacheLimit {
		copy(modelStatsValue.Details, modelStatsValue.Details[len(modelStatsValue.Details)-inMemoryDetailCacheLimit:])
		modelStatsValue.Details = modelStatsValue.Details[:inMemoryDetailCacheLimit]
	}
}

// Snapshot returns a copy of the aggregated metrics for external consumption.
func (s *RequestStatistics) Snapshot() StatisticsSnapshot {
	return s.SnapshotWithOptions(SnapshotOptions{DetailLimit: defaultSnapshotDetailCap})
}

// SnapshotWithOptions returns a copy of the aggregated metrics with optional detail truncation.
func (s *RequestStatistics) SnapshotWithOptions(options SnapshotOptions) StatisticsSnapshot {
	result := StatisticsSnapshot{}
	if s == nil {
		return result
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	result.TotalRequests = s.totalRequests
	result.SuccessCount = s.successCount
	result.FailureCount = s.failureCount
	result.TotalTokens = s.totalTokens

	result.APIs = make(map[string]APISnapshot, len(s.apis))
	for apiName, stats := range s.apis {
		apiSnapshot := APISnapshot{
			TotalRequests: stats.TotalRequests,
			TotalTokens:   stats.TotalTokens,
			Models:        make(map[string]ModelSnapshot, len(stats.Models)),
		}
		for modelName, modelStatsValue := range stats.Models {
			requestDetails, truncated := s.snapshotDetails(apiName, modelName, modelStatsValue.Details, options.DetailLimit)
			apiSnapshot.Models[modelName] = ModelSnapshot{
				TotalRequests:    modelStatsValue.TotalRequests,
				TotalTokens:      modelStatsValue.TotalTokens,
				Details:          requestDetails,
				DetailsTruncated: truncated,
				DetailsLimit:     detailLimitJSONValue(options.DetailLimit, truncated),
			}
		}
		result.APIs[apiName] = apiSnapshot
	}

	result.RequestsByDay = make(map[string]int64, len(s.requestsByDay))
	for k, v := range s.requestsByDay {
		result.RequestsByDay[k] = v
	}

	result.RequestsByHour = make(map[string]int64, len(s.requestsByHour))
	for hour, v := range s.requestsByHour {
		result.RequestsByHour[formatHour(hour)] = v
	}

	result.TokensByDay = make(map[string]int64, len(s.tokensByDay))
	for k, v := range s.tokensByDay {
		result.TokensByDay[k] = v
	}

	result.TokensByHour = make(map[string]int64, len(s.tokensByHour))
	for hour, v := range s.tokensByHour {
		result.TokensByHour[formatHour(hour)] = v
	}

	return result
}

func (s *RequestStatistics) snapshotDetails(apiName, modelName string, cached []RequestDetail, limit int) ([]RequestDetail, bool) {
	if s.detailStore != nil {
		details, truncated, err := s.detailStore.Details(apiName, modelName, limit)
		if err == nil {
			return details, truncated
		}
		log.Warnf("usagecompat: failed to query sqlite usage details: %v", err)
	}
	return copyRequestDetails(cached, limit)
}

func copyRequestDetails(details []RequestDetail, limit int) ([]RequestDetail, bool) {
	if limit < 0 || len(details) <= limit {
		requestDetails := make([]RequestDetail, len(details))
		copy(requestDetails, details)
		return requestDetails, false
	}
	if limit <= 0 {
		return []RequestDetail{}, len(details) > 0
	}
	start := len(details) - limit
	requestDetails := make([]RequestDetail, limit)
	copy(requestDetails, details[start:])
	return requestDetails, true
}

func detailLimitJSONValue(limit int, truncated bool) int {
	if !truncated {
		return 0
	}
	if limit < 0 {
		return 0
	}
	return limit
}

// MergeSnapshot merges an exported statistics snapshot into the current store.
func (s *RequestStatistics) MergeSnapshot(snapshot StatisticsSnapshot) MergeResult {
	result := MergeResult{}
	if s == nil {
		return result
	}

	if s.detailStore != nil {
		return s.mergeSnapshotWithStore(snapshot)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	seen := make(map[string]struct{})
	for apiName, stats := range s.apis {
		if stats == nil {
			continue
		}
		for modelName, modelStatsValue := range stats.Models {
			if modelStatsValue == nil {
				continue
			}
			for _, detail := range modelStatsValue.Details {
				seen[dedupKey(apiName, modelName, detail)] = struct{}{}
			}
		}
	}

	for apiName, apiSnapshot := range snapshot.APIs {
		apiName = strings.TrimSpace(apiName)
		if apiName == "" {
			continue
		}
		stats, ok := s.apis[apiName]
		if !ok || stats == nil {
			stats = &apiStats{Models: make(map[string]*modelStats)}
			s.apis[apiName] = stats
		} else if stats.Models == nil {
			stats.Models = make(map[string]*modelStats)
		}
		for modelName, modelSnapshot := range apiSnapshot.Models {
			modelName = strings.TrimSpace(modelName)
			if modelName == "" {
				modelName = "unknown"
			}
			for _, detail := range modelSnapshot.Details {
				detail.Tokens = normalizeTokenStats(detail.Tokens)
				if detail.LatencyMs < 0 {
					detail.LatencyMs = 0
				}
				if detail.Timestamp.IsZero() {
					detail.Timestamp = time.Now()
				}
				key := dedupKey(apiName, modelName, detail)
				if _, exists := seen[key]; exists {
					result.Skipped++
					continue
				}
				seen[key] = struct{}{}
				s.recordImported(apiName, modelName, stats, detail)
				result.Added++
			}
		}
	}

	return result
}

func (s *RequestStatistics) mergeSnapshotWithStore(snapshot StatisticsSnapshot) MergeResult {
	result := MergeResult{}
	for apiName, apiSnapshot := range snapshot.APIs {
		apiName = strings.TrimSpace(apiName)
		if apiName == "" {
			continue
		}
		for modelName, modelSnapshot := range apiSnapshot.Models {
			modelName = strings.TrimSpace(modelName)
			if modelName == "" {
				modelName = "unknown"
			}
			for _, detail := range modelSnapshot.Details {
				detail = normalizeRequestDetail(detail)
				inserted, err := s.detailStore.Insert(apiName, modelName, detail)
				if err != nil {
					log.Warnf("usagecompat: failed to import usage detail into sqlite: %v", err)
					result.Skipped++
					continue
				}
				if !inserted {
					result.Skipped++
					continue
				}
				s.mu.Lock()
				stats := s.ensureAPIStatsLocked(apiName)
				s.recordImported(apiName, modelName, stats, detail)
				s.mu.Unlock()
				result.Added++
			}
		}
	}
	return result
}

func (s *RequestStatistics) ensureAPIStatsLocked(apiName string) *apiStats {
	stats, ok := s.apis[apiName]
	if !ok || stats == nil {
		stats = &apiStats{Models: make(map[string]*modelStats)}
		s.apis[apiName] = stats
	} else if stats.Models == nil {
		stats.Models = make(map[string]*modelStats)
	}
	return stats
}

func (s *RequestStatistics) recordImported(apiName, modelName string, stats *apiStats, detail RequestDetail) {
	totalTokens := detail.Tokens.TotalTokens
	if totalTokens < 0 {
		totalTokens = 0
	}

	s.totalRequests++
	if detail.Failed {
		s.failureCount++
	} else {
		s.successCount++
	}
	s.totalTokens += totalTokens

	s.updateAPIStats(stats, modelName, detail)

	dayKey := detail.Timestamp.Format("2006-01-02")
	hourKey := detail.Timestamp.Hour()

	s.requestsByDay[dayKey]++
	s.requestsByHour[hourKey]++
	s.tokensByDay[dayKey] += totalTokens
	s.tokensByHour[hourKey] += totalTokens
}

// DetailsPage returns persisted usage details in newest-first order.
func (s *RequestStatistics) DetailsPage(query DetailPageQuery) DetailPage {
	normalized := normalizeDetailPageQuery(query)
	if s == nil {
		return emptyDetailPage(normalized)
	}
	if s.detailStore != nil {
		page, err := s.detailStore.Page(normalized)
		if err == nil {
			return page
		}
		log.Warnf("usagecompat: failed to query sqlite usage detail page: %v", err)
	}
	return s.memoryDetailsPage(normalized)
}

// Aggregate returns compact usage aggregates for charts and cost calculations.
func (s *RequestStatistics) Aggregate(query AggregateQuery) AggregateSnapshot {
	normalized := normalizeAggregateQuery(query)
	if s == nil {
		return emptyAggregateSnapshot(normalized)
	}
	if s.detailStore != nil {
		snapshot, err := s.detailStore.Aggregate(normalized)
		if err == nil {
			return snapshot
		}
		log.Warnf("usagecompat: failed to query sqlite usage aggregate: %v", err)
	}
	return s.memoryAggregate(normalized)
}

func (s *RequestStatistics) memoryAggregate(query AggregateQuery) AggregateSnapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()

	snapshot := emptyAggregateSnapshot(query)
	for apiName, stats := range s.apis {
		if stats == nil {
			continue
		}
		for modelName, modelStatsValue := range stats.Models {
			if modelStatsValue == nil {
				continue
			}
			for _, detail := range modelStatsValue.Details {
				if !aggregateDetailInRange(detail, query) {
					continue
				}
				addAggregateDetail(&snapshot, apiName, modelName, detail)
			}
		}
	}
	return snapshot
}

func (s *RequestStatistics) memoryDetailsPage(query DetailPageQuery) DetailPage {
	s.mu.RLock()
	defer s.mu.RUnlock()

	items := make([]UsageDetailRow, 0)
	for apiName, stats := range s.apis {
		if stats == nil {
			continue
		}
		for modelName, modelStatsValue := range stats.Models {
			if modelStatsValue == nil {
				continue
			}
			for _, detail := range modelStatsValue.Details {
				row := detailRowFromDetail(0, apiName, modelName, detail)
				if !detailRowMatchesQuery(row, query) {
					continue
				}
				items = append(items, row)
			}
		}
	}
	sortDetailRowsNewestFirst(items)
	total := int64(len(items))
	start := query.Offset
	if start > len(items) {
		start = len(items)
	}
	end := start + query.PageSize
	if end > len(items) {
		end = len(items)
	}
	return DetailPage{
		Items:    append([]UsageDetailRow(nil), items[start:end]...),
		Total:    total,
		Page:     query.Page,
		PageSize: query.PageSize,
		Offset:   query.Offset,
		HasMore:  end < len(items),
	}
}

func (s *RequestStatistics) loadDetailsFromStore() error {
	if s == nil || s.detailStore == nil {
		return nil
	}
	return s.detailStore.ForEach(func(apiName, modelName string, detail RequestDetail) {
		s.mu.Lock()
		stats := s.ensureAPIStatsLocked(apiName)
		s.recordImported(apiName, modelName, stats, detail)
		s.mu.Unlock()
	})
}

func normalizeRequestDetail(detail RequestDetail) RequestDetail {
	detail.Tokens = normalizeTokenStats(detail.Tokens)
	if detail.LatencyMs < 0 {
		detail.LatencyMs = 0
	}
	if detail.Timestamp.IsZero() {
		detail.Timestamp = time.Now()
	}
	return detail
}

func dedupKey(apiName, modelName string, detail RequestDetail) string {
	timestamp := detail.Timestamp.UTC().Format(time.RFC3339Nano)
	tokens := normalizeTokenStats(detail.Tokens)
	return fmt.Sprintf(
		"%s|%s|%s|%s|%s|%t|%d|%d|%d|%d|%d",
		apiName,
		modelName,
		timestamp,
		detail.Source,
		detail.AuthIndex,
		detail.Failed,
		tokens.InputTokens,
		tokens.OutputTokens,
		tokens.ReasoningTokens,
		tokens.CachedTokens,
		tokens.TotalTokens,
	)
}

func resolveAPIIdentifier(ctx context.Context, record coreusage.Record) string {
	apiKey := strings.TrimSpace(record.APIKey)
	if apiKey != "" {
		return apiKey
	}
	provider := strings.TrimSpace(record.Provider)
	if provider != "" {
		return provider
	}
	endpoint := strings.TrimSpace(internallogging.GetEndpoint(ctx))
	if endpoint != "" {
		return endpoint
	}
	return "unknown"
}

func resolveSuccess(ctx context.Context) bool {
	status := internallogging.GetResponseStatus(ctx)
	if status == 0 {
		return true
	}
	return status < httpStatusBadRequest
}

func normalizeDetail(detail coreusage.Detail) TokenStats {
	tokens := TokenStats{
		InputTokens:     detail.InputTokens,
		OutputTokens:    detail.OutputTokens,
		ReasoningTokens: detail.ReasoningTokens,
		CachedTokens:    detail.CachedTokens,
		TotalTokens:     detail.TotalTokens,
	}
	return normalizeTokenStats(tokens)
}

func normalizeTokenStats(tokens TokenStats) TokenStats {
	if tokens.TotalTokens == 0 {
		tokens.TotalTokens = tokens.InputTokens + tokens.OutputTokens + tokens.ReasoningTokens
	}
	if tokens.TotalTokens == 0 {
		tokens.TotalTokens = tokens.InputTokens + tokens.OutputTokens + tokens.ReasoningTokens + tokens.CachedTokens
	}
	return tokens
}

func normalizeAggregateQuery(query AggregateQuery) AggregateQuery {
	query.Range = strings.TrimSpace(strings.ToLower(query.Range))
	if query.Range == "" {
		query.Range = "all"
	}
	if query.Until.IsZero() {
		query.Until = time.Now()
	}
	if !query.Since.IsZero() && query.Since.After(query.Until) {
		query.Since = time.Time{}
	}
	return query
}

func emptyAggregateSnapshot(query AggregateQuery) AggregateSnapshot {
	query = normalizeAggregateQuery(query)
	snapshot := AggregateSnapshot{
		APIs:   make(map[string]AggregateAPI),
		Models: make(map[string]AggregateModel),
		Hourly: make([]AggregateBucket, 0),
		Daily:  make([]AggregateBucket, 0),
		Range:  query.Range,
	}
	if !query.Since.IsZero() {
		since := query.Since.UTC()
		snapshot.Since = &since
	}
	if !query.Until.IsZero() {
		until := query.Until.UTC()
		snapshot.Until = &until
	}
	return snapshot
}

func aggregateDetailInRange(detail RequestDetail, query AggregateQuery) bool {
	if detail.Timestamp.IsZero() {
		return false
	}
	if !query.Since.IsZero() && detail.Timestamp.Before(query.Since) {
		return false
	}
	if !query.Until.IsZero() && detail.Timestamp.After(query.Until) {
		return false
	}
	return true
}

func addAggregateDetail(snapshot *AggregateSnapshot, apiName, modelName string, detail RequestDetail) {
	if snapshot == nil {
		return
	}
	apiName = strings.TrimSpace(apiName)
	if apiName == "" {
		apiName = "unknown"
	}
	modelName = strings.TrimSpace(modelName)
	if modelName == "" {
		modelName = "unknown"
	}
	tokens := normalizeTokenStats(detail.Tokens)
	failed := detail.Failed
	addAggregateTotals(&snapshot.TotalRequests, &snapshot.SuccessCount, &snapshot.FailureCount, &snapshot.TotalTokens, &snapshot.Tokens, tokens, failed)

	apiValue := snapshot.APIs[apiName]
	if apiValue.Models == nil {
		apiValue.Models = make(map[string]AggregateModel)
	}
	addAggregateTotals(&apiValue.TotalRequests, &apiValue.SuccessCount, &apiValue.FailureCount, &apiValue.TotalTokens, &apiValue.Tokens, tokens, failed)
	modelValue := apiValue.Models[modelName]
	addAggregateTotals(&modelValue.TotalRequests, &modelValue.SuccessCount, &modelValue.FailureCount, &modelValue.TotalTokens, &modelValue.Tokens, tokens, failed)
	apiValue.Models[modelName] = modelValue
	snapshot.APIs[apiName] = apiValue

	globalModel := snapshot.Models[modelName]
	addAggregateTotals(&globalModel.TotalRequests, &globalModel.SuccessCount, &globalModel.FailureCount, &globalModel.TotalTokens, &globalModel.Tokens, tokens, failed)
	snapshot.Models[modelName] = globalModel
}

func addAggregateTotals(totalRequests, successCount, failureCount, totalTokens *int64, tokenTotals *TokenStats, tokens TokenStats, failed bool) {
	if totalRequests != nil {
		(*totalRequests)++
	}
	if failed {
		if failureCount != nil {
			(*failureCount)++
		}
	} else if successCount != nil {
		(*successCount)++
	}
	if totalTokens != nil {
		*totalTokens += tokens.TotalTokens
	}
	if tokenTotals != nil {
		tokenTotals.InputTokens += tokens.InputTokens
		tokenTotals.OutputTokens += tokens.OutputTokens
		tokenTotals.ReasoningTokens += tokens.ReasoningTokens
		tokenTotals.CachedTokens += tokens.CachedTokens
		tokenTotals.TotalTokens += tokens.TotalTokens
	}
}

func normalizeLatency(latency time.Duration) int64 {
	if latency <= 0 {
		return 0
	}
	return latency.Milliseconds()
}

func formatHour(hour int) string {
	if hour < 0 {
		hour = 0
	}
	hour = hour % 24
	return fmt.Sprintf("%02d", hour)
}

const httpStatusBadRequest = 400
