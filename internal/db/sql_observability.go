package db

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"github.com/dleiferives/tifl/internal/domain"
)

// Observability queries (#24): the global LLM call log and its cost/token
// aggregations. These are the admin read surface — cross-user by design, unlike
// ListSessionLLMCalls which is tenant-scoped. Cost is never computed here; the
// caller multiplies these token sums by configured pricing at query time.

const llmCallListColumns = `call_id, session_id, user_id, kind, prompt_version, model,
	input_tokens, output_tokens, latency_ms, status, error_detail, called_at`

// llmFilterClause renders a domain.LLMCallFilter into a WHERE fragment (empty
// string when unfiltered) and its ordered arguments.
func llmFilterClause(f domain.LLMCallFilter) (string, []any) {
	var conds []string
	var args []any
	eq := func(col, val string) {
		if val != "" {
			conds = append(conds, col+" = ?")
			args = append(args, val)
		}
	}
	eq("user_id", f.UserID)
	eq("session_id", f.SessionID)
	eq("model", f.Model)
	eq("kind", f.Kind)
	eq("status", f.Status)
	eq("prompt_version", f.PromptVersion)
	if f.Since > 0 {
		conds = append(conds, "called_at >= ?")
		args = append(args, f.Since)
	}
	if f.Until > 0 {
		conds = append(conds, "called_at < ?")
		args = append(args, f.Until)
	}
	if len(conds) == 0 {
		return "", nil
	}
	return " WHERE " + strings.Join(conds, " AND "), args
}

// ListLLMCalls returns filtered call-log rows newest first, without the payload
// columns (prompts/responses) so the list stays cheap and logs do not leak into
// casual scrolling — payloads are fetched per call via GetLLMCall. Limit is
// clamped by the caller; Offset paginates.
func (r *SQLRepository) ListLLMCalls(ctx context.Context, f domain.LLMCallFilter) ([]domain.LLMCall, error) {
	where, args := llmFilterClause(f)
	limit := f.Limit
	if limit <= 0 {
		limit = 50
	}
	query := `SELECT ` + llmCallListColumns + `
		   FROM llm_calls` + where + `
		  ORDER BY called_at DESC, call_id DESC
		  LIMIT ? OFFSET ?`
	args = append(args, limit, f.Offset)
	rows, err := r.query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.LLMCall
	for rows.Next() {
		call, err := scanLLMCallListRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, call)
	}
	return out, rows.Err()
}

// GetLLMCall returns one call by id with its full payload columns, for the admin
// per-call detail view. ErrNotFound when the id is unknown.
func (r *SQLRepository) GetLLMCall(ctx context.Context, callID string) (domain.LLMCall, error) {
	call, err := scanSQLiteLLMCall(r.queryRow(ctx,
		`SELECT call_id, session_id, user_id, kind, prompt_version, model,
		        input_tokens, output_tokens, latency_ms, status, error_detail,
		        system_prompt, user_prompt, raw_response, parsed_output, error_payload, called_at
		   FROM llm_calls WHERE call_id = ?`, callID))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.LLMCall{}, ErrNotFound
	}
	return call, err
}

// ListSessionLLMCallsAll returns every call for a session regardless of owner,
// with full payloads — the admin cross-user session debug view. Regular users
// go through the tenant-scoped ListSessionLLMCalls.
func (r *SQLRepository) ListSessionLLMCallsAll(ctx context.Context, sessionID string) ([]domain.LLMCall, error) {
	rows, err := r.query(ctx,
		`SELECT call_id, session_id, user_id, kind, prompt_version, model,
		        input_tokens, output_tokens, latency_ms, status, error_detail,
		        system_prompt, user_prompt, raw_response, parsed_output, error_payload, called_at
		   FROM llm_calls
		  WHERE session_id = ?
		  ORDER BY called_at, call_id`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.LLMCall
	for rows.Next() {
		call, err := scanSQLiteLLMCall(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, call)
	}
	return out, rows.Err()
}

// AggregateLLMTokens sums call counts and input/output tokens over llm_calls,
// grouped by the dimensions named in group (per session, per day, per model,
// per kind, or any combination). It is the single GROUP BY behind every cost
// rollup; the caller folds and prices the buckets. At least one grouping
// dimension is required.
func (r *SQLRepository) AggregateLLMTokens(ctx context.Context, f domain.LLMCallFilter, group domain.LLMTokenGroup) ([]domain.LLMTokenAggregate, error) {
	var selCols, groupCols []string
	dayExpr := r.d.dayBucketExpr("called_at")
	if group.Day {
		selCols = append(selCols, dayExpr)
		groupCols = append(groupCols, dayExpr)
	}
	if group.Model {
		selCols = append(selCols, "model")
		groupCols = append(groupCols, "model")
	}
	if group.Kind {
		selCols = append(selCols, "kind")
		groupCols = append(groupCols, "kind")
	}
	if group.Session {
		selCols = append(selCols, "session_id")
		groupCols = append(groupCols, "session_id")
	}
	if len(groupCols) == 0 {
		return nil, errors.New("AggregateLLMTokens: at least one grouping dimension is required")
	}

	where, args := llmFilterClause(f)
	query := `SELECT ` + strings.Join(selCols, ", ") +
		`, COUNT(*), COALESCE(SUM(COALESCE(input_tokens, 0)), 0), COALESCE(SUM(COALESCE(output_tokens, 0)), 0)
		   FROM llm_calls` + where +
		` GROUP BY ` + strings.Join(groupCols, ", ") +
		` ORDER BY ` + strings.Join(groupCols, ", ")

	rows, err := r.query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []domain.LLMTokenAggregate
	for rows.Next() {
		var (
			day                 float64
			model, kind         string
			sessionID           sql.NullString
			calls               int
			inputSum, outputSum int64
		)
		dest := make([]any, 0, len(selCols)+3)
		if group.Day {
			dest = append(dest, &day)
		}
		if group.Model {
			dest = append(dest, &model)
		}
		if group.Kind {
			dest = append(dest, &kind)
		}
		if group.Session {
			dest = append(dest, &sessionID)
		}
		dest = append(dest, &calls, &inputSum, &outputSum)
		if err := rows.Scan(dest...); err != nil {
			return nil, err
		}
		agg := domain.LLMTokenAggregate{Calls: calls, InputTokens: inputSum, OutputTokens: outputSum}
		if group.Day {
			agg.Day = int64(day)
		}
		if group.Model {
			agg.Model = model
		}
		if group.Kind {
			agg.Kind = kind
		}
		if group.Session {
			agg.SessionID = sessionID.String
		}
		out = append(out, agg)
	}
	return out, rows.Err()
}

// scanLLMCallListRow scans a payload-free call-log row (see llmCallListColumns).
func scanLLMCallListRow(row interface {
	Scan(dest ...any) error
}) (domain.LLMCall, error) {
	var (
		call                   domain.LLMCall
		sessionID, userID      sql.NullString
		errText                sql.NullString
		input, output, latency sql.NullInt64
	)
	if err := row.Scan(
		&call.CallID, &sessionID, &userID, &call.Kind, &call.PromptVersion, &call.Model,
		&input, &output, &latency, &call.Status, &errText, &call.CalledAt,
	); err != nil {
		return domain.LLMCall{}, err
	}
	call.SessionID = stringPtrFromNull(sessionID)
	call.UserID = stringPtrFromNull(userID)
	call.InputTokens = intPtrFromNull(input)
	call.OutputTokens = intPtrFromNull(output)
	call.LatencyMs = intPtrFromNull(latency)
	call.ErrorDetail = stringPtrFromNull(errText)
	return call, nil
}
