package usagecompat

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

const (
	usageSQLitePathEnv     = "USAGECOMPAT_SQLITE_PATH"
	defaultUsageSQLitePath = "usagecompat.sqlite3"
	defaultDetailPageSize  = 100
	maxDetailPageSize      = 1000
	sqliteFailedTrue       = 1
	sqliteFailedFalse      = 0
	hourBucketNanoseconds  = int64(time.Hour)
	dayBucketNanoseconds   = int64(24 * time.Hour)
)

type sqliteDetailStore struct {
	db *sql.DB
}

func defaultSQLiteDetailStorePath() string {
	if path := strings.TrimSpace(os.Getenv(usageSQLitePathEnv)); path != "" {
		return path
	}
	return defaultUsageSQLitePath
}

func newSQLiteDetailStore(path string) (*sqliteDetailStore, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, fmt.Errorf("empty sqlite path")
	}
	if dir := filepath.Dir(path); dir != "." && dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("create sqlite directory: %w", err)
		}
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite detail store: %w", err)
	}
	db.SetMaxOpenConns(1)
	store := &sqliteDetailStore{db: db}
	if err := store.init(); err != nil {
		if closeErr := db.Close(); closeErr != nil {
			return nil, fmt.Errorf("init sqlite detail store: %w; close sqlite: %v", err, closeErr)
		}
		return nil, err
	}
	return store, nil
}

func (s *sqliteDetailStore) init() error {
	if s == nil || s.db == nil {
		return nil
	}
	statements := []string{
		`PRAGMA journal_mode = WAL`,
		`PRAGMA synchronous = NORMAL`,
		`PRAGMA busy_timeout = 5000`,
		`CREATE TABLE IF NOT EXISTS usage_details (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			api_name TEXT NOT NULL,
			model_name TEXT NOT NULL,
			timestamp_ns INTEGER NOT NULL,
			latency_ms INTEGER NOT NULL,
			client_ip TEXT NOT NULL DEFAULT '',
			source TEXT NOT NULL,
			auth_index TEXT NOT NULL,
			reasoning_effort TEXT NOT NULL DEFAULT '',
			service_tier TEXT NOT NULL DEFAULT '',
			applied_service_tier TEXT NOT NULL DEFAULT '',
			response_service_tier TEXT NOT NULL DEFAULT '',
			input_tokens INTEGER NOT NULL,
			output_tokens INTEGER NOT NULL,
			reasoning_tokens INTEGER NOT NULL,
			cached_tokens INTEGER NOT NULL,
			total_tokens INTEGER NOT NULL,
			failed INTEGER NOT NULL,
			dedup_key TEXT NOT NULL UNIQUE
		)`,
		`ALTER TABLE usage_details ADD COLUMN reasoning_effort TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE usage_details ADD COLUMN client_ip TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE usage_details ADD COLUMN service_tier TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE usage_details ADD COLUMN applied_service_tier TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE usage_details ADD COLUMN response_service_tier TEXT NOT NULL DEFAULT ''`,
		`CREATE INDEX IF NOT EXISTS usage_details_api_model_time_idx ON usage_details(api_name, model_name, timestamp_ns DESC, id DESC)`,
		`CREATE INDEX IF NOT EXISTS usage_details_time_idx ON usage_details(timestamp_ns DESC, id DESC)`,
	}
	for _, statement := range statements {
		if _, err := s.db.Exec(statement); err != nil {
			if strings.Contains(statement, "ADD COLUMN") && strings.Contains(strings.ToLower(err.Error()), "duplicate column") {
				continue
			}
			return fmt.Errorf("sqlite init statement failed: %w", err)
		}
	}
	return nil
}

func (s *sqliteDetailStore) Insert(apiName, modelName string, detail RequestDetail) (bool, error) {
	if s == nil || s.db == nil {
		return true, nil
	}
	detail = normalizeRequestDetail(detail)
	tokens := normalizeTokenStats(detail.Tokens)
	failed := sqliteFailedFalse
	if detail.Failed {
		failed = sqliteFailedTrue
	}
	_, err := s.db.Exec(
		`INSERT INTO usage_details (
			api_name, model_name, timestamp_ns, latency_ms, client_ip, source, auth_index, reasoning_effort,
			service_tier, applied_service_tier, response_service_tier,
			input_tokens, output_tokens, reasoning_tokens, cached_tokens, total_tokens,
			failed, dedup_key
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		apiName,
		modelName,
		detail.Timestamp.UTC().UnixNano(),
		detail.LatencyMs,
		detail.ClientIP,
		detail.Source,
		detail.AuthIndex,
		detail.ReasoningEffort,
		detail.ServiceTier,
		detail.AppliedTier,
		detail.ResponseTier,
		tokens.InputTokens,
		tokens.OutputTokens,
		tokens.ReasoningTokens,
		tokens.CachedTokens,
		tokens.TotalTokens,
		failed,
		dedupKey(apiName, modelName, detail),
	)
	if err != nil {
		if isSQLiteDuplicate(err) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

func (s *sqliteDetailStore) Details(apiName, modelName string, limit int) ([]RequestDetail, bool, error) {
	if s == nil || s.db == nil {
		return nil, false, nil
	}
	query := DetailPageQuery{API: apiName, Model: modelName}
	if limit < 0 {
		rows, err := s.queryDetails(query, -1, 0, false)
		return rowsToDetails(rows), false, err
	}
	total, err := s.count(query)
	if err != nil {
		return nil, false, err
	}
	rows, err := s.queryDetails(query, limit, 0, true)
	if err != nil {
		return nil, false, err
	}
	sortDetailRowsOldestFirst(rows)
	return rowsToDetails(rows), total > int64(limit), nil
}

func (s *sqliteDetailStore) Page(query DetailPageQuery) (DetailPage, error) {
	normalized := normalizeDetailPageQuery(query)
	total, err := s.count(normalized)
	if err != nil {
		return emptyDetailPage(normalized), err
	}
	rows, err := s.queryDetails(normalized, normalized.PageSize, normalized.Offset, true)
	if err != nil {
		return emptyDetailPage(normalized), err
	}
	return DetailPage{
		Items:    rows,
		Total:    total,
		Page:     normalized.Page,
		PageSize: normalized.PageSize,
		Offset:   normalized.Offset,
		HasMore:  int64(normalized.Offset+len(rows)) < total,
	}, nil
}

func (s *sqliteDetailStore) Aggregate(query AggregateQuery) (AggregateSnapshot, error) {
	normalized := normalizeAggregateQuery(query)
	snapshot := emptyAggregateSnapshot(normalized)
	if s == nil || s.db == nil {
		return snapshot, nil
	}
	where, args := aggregateWhereClause(normalized)
	if err := s.scanAggregateTotals(&snapshot, where, args); err != nil {
		return snapshot, err
	}
	snapshot.TotalTokens = snapshot.Tokens.TotalTokens
	if err := s.scanAggregateAPIModels(&snapshot, where, args); err != nil {
		return snapshot, err
	}
	hourly, err := s.scanAggregateBuckets(where, args, hourBucketNanoseconds)
	if err != nil {
		return snapshot, err
	}
	daily, err := s.scanAggregateBuckets(where, args, dayBucketNanoseconds)
	if err != nil {
		return snapshot, err
	}
	snapshot.Hourly = hourly
	snapshot.Daily = daily
	return snapshot, nil
}

func (s *sqliteDetailStore) ForEach(fn func(apiName, modelName string, detail RequestDetail)) error {
	if s == nil || s.db == nil || fn == nil {
		return nil
	}
	rows, err := s.db.Query(
		`SELECT id, api_name, model_name, timestamp_ns, latency_ms, client_ip, source, auth_index, reasoning_effort,
			service_tier, applied_service_tier, response_service_tier,
			input_tokens, output_tokens, reasoning_tokens, cached_tokens, total_tokens, failed
		FROM usage_details
		ORDER BY timestamp_ns ASC, id ASC`,
	)
	if err != nil {
		return err
	}
	defer func() {
		_ = rows.Close()
	}()
	for rows.Next() {
		row, err := scanDetailRow(rows)
		if err != nil {
			return err
		}
		fn(row.API, row.Model, detailFromRow(row))
	}
	return rows.Err()
}

func (s *sqliteDetailStore) scanAggregateTotals(snapshot *AggregateSnapshot, where string, args []any) error {
	row := s.db.QueryRow(`SELECT
		COUNT(*),
		COALESCE(SUM(CASE WHEN failed = 0 THEN 1 ELSE 0 END), 0),
		COALESCE(SUM(CASE WHEN failed != 0 THEN 1 ELSE 0 END), 0),
		COALESCE(SUM(input_tokens), 0),
		COALESCE(SUM(output_tokens), 0),
		COALESCE(SUM(reasoning_tokens), 0),
		COALESCE(SUM(cached_tokens), 0),
		COALESCE(SUM(total_tokens), 0)
		FROM usage_details `+where, args...)
	return row.Scan(
		&snapshot.TotalRequests,
		&snapshot.SuccessCount,
		&snapshot.FailureCount,
		&snapshot.Tokens.InputTokens,
		&snapshot.Tokens.OutputTokens,
		&snapshot.Tokens.ReasoningTokens,
		&snapshot.Tokens.CachedTokens,
		&snapshot.Tokens.TotalTokens,
	)
}

func (s *sqliteDetailStore) scanAggregateAPIModels(snapshot *AggregateSnapshot, where string, args []any) error {
	rows, err := s.db.Query(`SELECT
		api_name,
		model_name,
		COUNT(*),
		COALESCE(SUM(CASE WHEN failed = 0 THEN 1 ELSE 0 END), 0),
		COALESCE(SUM(CASE WHEN failed != 0 THEN 1 ELSE 0 END), 0),
		COALESCE(SUM(input_tokens), 0),
		COALESCE(SUM(output_tokens), 0),
		COALESCE(SUM(reasoning_tokens), 0),
		COALESCE(SUM(cached_tokens), 0),
		COALESCE(SUM(total_tokens), 0)
		FROM usage_details `+where+`
		GROUP BY api_name, model_name
		ORDER BY api_name ASC, model_name ASC`, args...)
	if err != nil {
		return err
	}
	defer func() {
		_ = rows.Close()
	}()
	for rows.Next() {
		var apiName string
		var modelName string
		var model AggregateModel
		if err := rows.Scan(
			&apiName,
			&modelName,
			&model.TotalRequests,
			&model.SuccessCount,
			&model.FailureCount,
			&model.Tokens.InputTokens,
			&model.Tokens.OutputTokens,
			&model.Tokens.ReasoningTokens,
			&model.Tokens.CachedTokens,
			&model.Tokens.TotalTokens,
		); err != nil {
			return err
		}
		model.TotalTokens = model.Tokens.TotalTokens
		apiValue := snapshot.APIs[apiName]
		if apiValue.Models == nil {
			apiValue.Models = make(map[string]AggregateModel)
		}
		apiValue.Models[modelName] = model
		addAggregateModelToAPI(&apiValue, model)
		snapshot.APIs[apiName] = apiValue
		global := snapshot.Models[modelName]
		mergeAggregateModel(&global, model)
		snapshot.Models[modelName] = global
	}
	return rows.Err()
}

func (s *sqliteDetailStore) scanAggregateBuckets(where string, args []any, bucketNS int64) ([]AggregateBucket, error) {
	queryArgs := make([]any, 0, len(args)+2)
	queryArgs = append(queryArgs, bucketNS, bucketNS)
	queryArgs = append(queryArgs, args...)
	rows, err := s.db.Query(`SELECT
		((timestamp_ns / ?) * ?) AS bucket_ns,
		api_name,
		model_name,
		COUNT(*),
		COALESCE(SUM(CASE WHEN failed = 0 THEN 1 ELSE 0 END), 0),
		COALESCE(SUM(CASE WHEN failed != 0 THEN 1 ELSE 0 END), 0),
		COALESCE(SUM(input_tokens), 0),
		COALESCE(SUM(output_tokens), 0),
		COALESCE(SUM(reasoning_tokens), 0),
		COALESCE(SUM(cached_tokens), 0),
		COALESCE(SUM(total_tokens), 0)
		FROM usage_details `+where+`
		GROUP BY bucket_ns, api_name, model_name
		ORDER BY bucket_ns ASC, api_name ASC, model_name ASC`, queryArgs...)
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = rows.Close()
	}()
	buckets := make([]AggregateBucket, 0)
	for rows.Next() {
		var bucketNSValue int64
		var bucket AggregateBucket
		if err := rows.Scan(
			&bucketNSValue,
			&bucket.API,
			&bucket.Model,
			&bucket.TotalRequests,
			&bucket.SuccessCount,
			&bucket.FailureCount,
			&bucket.Tokens.InputTokens,
			&bucket.Tokens.OutputTokens,
			&bucket.Tokens.ReasoningTokens,
			&bucket.Tokens.CachedTokens,
			&bucket.Tokens.TotalTokens,
		); err != nil {
			return nil, err
		}
		bucket.TotalTokens = bucket.Tokens.TotalTokens
		bucket.Bucket = time.Unix(0, bucketNSValue).UTC()
		buckets = append(buckets, bucket)
	}
	return buckets, rows.Err()
}

func (s *sqliteDetailStore) count(query DetailPageQuery) (int64, error) {
	where, args := detailWhereClause(query)
	var total int64
	err := s.db.QueryRow(`SELECT COUNT(*) FROM usage_details `+where, args...).Scan(&total)
	return total, err
}

func (s *sqliteDetailStore) queryDetails(query DetailPageQuery, limit, offset int, newestFirst bool) ([]UsageDetailRow, error) {
	where, args := detailWhereClause(query)
	order := "ORDER BY timestamp_ns ASC, id ASC"
	if newestFirst {
		order = "ORDER BY timestamp_ns DESC, id DESC"
	}
	sqlQuery := `SELECT id, api_name, model_name, timestamp_ns, latency_ms, client_ip, source, auth_index, reasoning_effort,
		service_tier, applied_service_tier, response_service_tier,
		input_tokens, output_tokens, reasoning_tokens, cached_tokens, total_tokens, failed
		FROM usage_details ` + where + " " + order
	if limit >= 0 {
		sqlQuery += " LIMIT ? OFFSET ?"
		args = append(args, limit, offset)
	}
	rows, err := s.db.Query(sqlQuery, args...)
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = rows.Close()
	}()
	result := make([]UsageDetailRow, 0)
	for rows.Next() {
		row, err := scanDetailRow(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return result, nil
}

func detailWhereClause(query DetailPageQuery) (string, []any) {
	query = normalizeDetailPageQuery(query)
	conditions := make([]string, 0, 5)
	args := make([]any, 0, 7)
	if query.API != "" {
		conditions = append(conditions, "api_name = ?")
		args = append(args, query.API)
	}
	if query.Model != "" {
		conditions = append(conditions, "model_name = ?")
		args = append(args, query.Model)
	}
	if query.Source != "" {
		conditions = append(conditions, "source = ?")
		args = append(args, query.Source)
	}
	if query.AuthIndex != "" {
		conditions = append(conditions, "auth_index = ?")
		args = append(args, query.AuthIndex)
	}
	if query.Search != "" {
		conditions = append(conditions, "(api_name LIKE ? ESCAPE '\\' OR model_name LIKE ? ESCAPE '\\' OR client_ip LIKE ? ESCAPE '\\' OR source LIKE ? ESCAPE '\\' OR auth_index LIKE ? ESCAPE '\\' OR service_tier LIKE ? ESCAPE '\\' OR applied_service_tier LIKE ? ESCAPE '\\' OR response_service_tier LIKE ? ESCAPE '\\')")
		searchArg := "%" + escapeSQLiteLike(query.Search) + "%"
		args = append(args, searchArg, searchArg, searchArg, searchArg, searchArg, searchArg, searchArg, searchArg)
	}
	if len(conditions) == 0 {
		return "", args
	}
	return "WHERE " + strings.Join(conditions, " AND "), args
}

func aggregateWhereClause(query AggregateQuery) (string, []any) {
	query = normalizeAggregateQuery(query)
	conditions := make([]string, 0, 2)
	args := make([]any, 0, 2)
	if !query.Since.IsZero() {
		conditions = append(conditions, "timestamp_ns >= ?")
		args = append(args, query.Since.UTC().UnixNano())
	}
	if !query.Until.IsZero() {
		conditions = append(conditions, "timestamp_ns <= ?")
		args = append(args, query.Until.UTC().UnixNano())
	}
	if len(conditions) == 0 {
		return "", args
	}
	return "WHERE " + strings.Join(conditions, " AND "), args
}

func addAggregateModelToAPI(apiValue *AggregateAPI, model AggregateModel) {
	if apiValue == nil {
		return
	}
	apiValue.TotalRequests += model.TotalRequests
	apiValue.SuccessCount += model.SuccessCount
	apiValue.FailureCount += model.FailureCount
	apiValue.TotalTokens += model.TotalTokens
	apiValue.Tokens.InputTokens += model.Tokens.InputTokens
	apiValue.Tokens.OutputTokens += model.Tokens.OutputTokens
	apiValue.Tokens.ReasoningTokens += model.Tokens.ReasoningTokens
	apiValue.Tokens.CachedTokens += model.Tokens.CachedTokens
	apiValue.Tokens.TotalTokens += model.Tokens.TotalTokens
}

func mergeAggregateModel(target *AggregateModel, source AggregateModel) {
	if target == nil {
		return
	}
	target.TotalRequests += source.TotalRequests
	target.SuccessCount += source.SuccessCount
	target.FailureCount += source.FailureCount
	target.TotalTokens += source.TotalTokens
	target.Tokens.InputTokens += source.Tokens.InputTokens
	target.Tokens.OutputTokens += source.Tokens.OutputTokens
	target.Tokens.ReasoningTokens += source.Tokens.ReasoningTokens
	target.Tokens.CachedTokens += source.Tokens.CachedTokens
	target.Tokens.TotalTokens += source.Tokens.TotalTokens
}

func scanDetailRow(rows interface {
	Scan(dest ...any) error
}) (UsageDetailRow, error) {
	var row UsageDetailRow
	var timestampNS int64
	var failed int
	err := rows.Scan(
		&row.ID,
		&row.API,
		&row.Model,
		&timestampNS,
		&row.LatencyMs,
		&row.ClientIP,
		&row.Source,
		&row.AuthIndex,
		&row.ReasoningEffort,
		&row.ServiceTier,
		&row.AppliedTier,
		&row.ResponseTier,
		&row.Tokens.InputTokens,
		&row.Tokens.OutputTokens,
		&row.Tokens.ReasoningTokens,
		&row.Tokens.CachedTokens,
		&row.Tokens.TotalTokens,
		&failed,
	)
	if err != nil {
		return row, err
	}
	row.Timestamp = time.Unix(0, timestampNS).UTC()
	row.Failed = failed != 0
	row.Tokens = normalizeTokenStats(row.Tokens)
	return row, nil
}

func rowsToDetails(rows []UsageDetailRow) []RequestDetail {
	details := make([]RequestDetail, 0, len(rows))
	for _, row := range rows {
		details = append(details, detailFromRow(row))
	}
	return details
}

func detailFromRow(row UsageDetailRow) RequestDetail {
	return RequestDetail{
		Timestamp:       row.Timestamp,
		LatencyMs:       row.LatencyMs,
		ClientIP:        row.ClientIP,
		Source:          row.Source,
		AuthIndex:       row.AuthIndex,
		ReasoningEffort: row.ReasoningEffort,
		ServiceTier:     row.ServiceTier,
		AppliedTier:     row.AppliedTier,
		ResponseTier:    row.ResponseTier,
		Tokens:          row.Tokens,
		Failed:          row.Failed,
	}
}

func detailRowFromDetail(id int64, apiName, modelName string, detail RequestDetail) UsageDetailRow {
	detail = normalizeRequestDetail(detail)
	return UsageDetailRow{
		ID:              id,
		API:             apiName,
		Model:           modelName,
		Timestamp:       detail.Timestamp,
		LatencyMs:       detail.LatencyMs,
		ClientIP:        detail.ClientIP,
		Source:          detail.Source,
		AuthIndex:       detail.AuthIndex,
		ReasoningEffort: detail.ReasoningEffort,
		ServiceTier:     detail.ServiceTier,
		AppliedTier:     detail.AppliedTier,
		ResponseTier:    detail.ResponseTier,
		Tokens:          detail.Tokens,
		Failed:          detail.Failed,
	}
}

func normalizeDetailPageQuery(query DetailPageQuery) DetailPageQuery {
	query.API = strings.TrimSpace(query.API)
	query.Model = strings.TrimSpace(query.Model)
	query.Source = strings.TrimSpace(query.Source)
	query.AuthIndex = strings.TrimSpace(query.AuthIndex)
	query.Search = strings.TrimSpace(query.Search)
	if query.PageSize <= 0 {
		query.PageSize = defaultDetailPageSize
	}
	if query.PageSize > maxDetailPageSize {
		query.PageSize = maxDetailPageSize
	}
	if query.Page <= 0 {
		query.Page = 1
	}
	if query.Offset < 0 {
		query.Offset = 0
	}
	if query.Offset == 0 && query.Page > 1 {
		query.Offset = (query.Page - 1) * query.PageSize
	}
	return query
}

func detailRowMatchesQuery(row UsageDetailRow, query DetailPageQuery) bool {
	query = normalizeDetailPageQuery(query)
	if query.API != "" && row.API != query.API {
		return false
	}
	if query.Model != "" && row.Model != query.Model {
		return false
	}
	if query.Source != "" && row.Source != query.Source {
		return false
	}
	if query.AuthIndex != "" && row.AuthIndex != query.AuthIndex {
		return false
	}
	if query.Search == "" {
		return true
	}
	search := strings.ToLower(query.Search)
	return strings.Contains(strings.ToLower(row.API), search) ||
		strings.Contains(strings.ToLower(row.Model), search) ||
		strings.Contains(strings.ToLower(row.ClientIP), search) ||
		strings.Contains(strings.ToLower(row.Source), search) ||
		strings.Contains(strings.ToLower(row.AuthIndex), search) ||
		strings.Contains(strings.ToLower(row.ServiceTier), search) ||
		strings.Contains(strings.ToLower(row.AppliedTier), search) ||
		strings.Contains(strings.ToLower(row.ResponseTier), search)
}

func escapeSQLiteLike(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, `%`, `\%`)
	value = strings.ReplaceAll(value, `_`, `\_`)
	return value
}

func emptyDetailPage(query DetailPageQuery) DetailPage {
	query = normalizeDetailPageQuery(query)
	return DetailPage{
		Items:    []UsageDetailRow{},
		Total:    0,
		Page:     query.Page,
		PageSize: query.PageSize,
		Offset:   query.Offset,
		HasMore:  false,
	}
}

func sortDetailRowsNewestFirst(rows []UsageDetailRow) {
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].Timestamp.Equal(rows[j].Timestamp) {
			return rows[i].ID > rows[j].ID
		}
		return rows[i].Timestamp.After(rows[j].Timestamp)
	})
}

func sortDetailRowsOldestFirst(rows []UsageDetailRow) {
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].Timestamp.Equal(rows[j].Timestamp) {
			return rows[i].ID < rows[j].ID
		}
		return rows[i].Timestamp.Before(rows[j].Timestamp)
	})
}

func isSQLiteDuplicate(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "constraint")
}
