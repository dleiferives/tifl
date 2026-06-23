package db

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/dleiferives/tifl/internal/domain"
	"github.com/dleiferives/tifl/internal/id"
)

func (r *PostgresRepository) UpsertSkill(ctx context.Context, skill domain.Skill) error {
	_, err := r.pool.Exec(ctx,
		`INSERT INTO skills(skill_id, language, name, description, category, tier_count, xp_per_tier, sort_order)
		 VALUES($1, $2, $3, $4, $5, $6, $7, $8)
		 ON CONFLICT(skill_id) DO UPDATE SET
		   language = excluded.language,
		   name = excluded.name,
		   description = excluded.description,
		   category = excluded.category,
		   tier_count = excluded.tier_count,
		   xp_per_tier = excluded.xp_per_tier,
		   sort_order = excluded.sort_order`,
		skill.SkillID, skill.Language, skill.Name, nullStr(skill.Description), skill.Category,
		skill.TierCount, skill.XPPerTier, skill.SortOrder)
	return err
}

func (r *PostgresRepository) ListSkills(ctx context.Context, language string) ([]domain.Skill, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT skill_id, language, name, description, category, tier_count, xp_per_tier, sort_order
		 FROM skills WHERE language = $1
		 ORDER BY category, sort_order IS NULL, sort_order, name, skill_id`, language)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanPgSkills(rows)
}

func (r *PostgresRepository) GetSkill(ctx context.Context, skillID string) (domain.Skill, error) {
	skill, err := scanPgSkill(r.pool.QueryRow(ctx,
		`SELECT skill_id, language, name, description, category, tier_count, xp_per_tier, sort_order
		 FROM skills WHERE skill_id = $1`, skillID))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Skill{}, ErrNotFound
	}
	return skill, err
}

func (r *PostgresRepository) UpsertItemSkillAssociations(ctx context.Context, itemID string, skillIDs []string) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, `DELETE FROM item_skill_associations WHERE item_id = $1`, itemID); err != nil {
		return err
	}
	for _, skillID := range uniqueStrings(skillIDs) {
		if _, err := tx.Exec(ctx,
			`INSERT INTO item_skill_associations(item_id, skill_id) VALUES($1, $2)`,
			itemID, skillID); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func (r *PostgresRepository) ListItemSkillAssociations(ctx context.Context, itemIDs []string) ([]domain.ItemSkillAssociation, error) {
	itemIDs = uniqueStrings(itemIDs)
	if len(itemIDs) == 0 {
		return nil, nil
	}
	args := make([]any, len(itemIDs))
	for i, itemID := range itemIDs {
		args[i] = itemID
	}
	rows, err := r.pool.Query(ctx,
		`SELECT item_id, skill_id FROM item_skill_associations
		 WHERE item_id IN (`+pgPlaceholders(1, len(itemIDs))+`)
		 ORDER BY item_id, skill_id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanPgItemSkillAssociations(rows)
}

func (r *PostgresRepository) ListSkillAssociations(ctx context.Context, skillID string) ([]domain.ItemSkillAssociation, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT item_id, skill_id FROM item_skill_associations
		 WHERE skill_id = $1 ORDER BY item_id`, skillID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanPgItemSkillAssociations(rows)
}

func (r *PostgresRepository) GetUserSkillXP(ctx context.Context, userID, skillID string) (domain.UserSkillXP, error) {
	xp, err := scanPgUserSkillXP(r.pool.QueryRow(ctx,
		`SELECT user_id, skill_id, xp, tier, pending_verify, last_verified_at, updated_at
		 FROM user_skill_xp WHERE user_id = $1 AND skill_id = $2`, userID, skillID))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.UserSkillXP{}, ErrNotFound
	}
	return xp, err
}

func (r *PostgresRepository) ListUserSkillXP(ctx context.Context, userID string, skillIDs []string) ([]domain.UserSkillXP, error) {
	skillIDs = uniqueStrings(skillIDs)
	query := `SELECT user_id, skill_id, xp, tier, pending_verify, last_verified_at, updated_at
	          FROM user_skill_xp WHERE user_id = $1`
	args := []any{userID}
	if len(skillIDs) > 0 {
		for _, skillID := range skillIDs {
			args = append(args, skillID)
		}
		query += ` AND skill_id IN (` + pgPlaceholders(2, len(skillIDs)) + `)`
	}
	query += ` ORDER BY skill_id`
	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanPgUserSkillXPs(rows)
}

func (r *PostgresRepository) UpsertUserSkillXP(ctx context.Context, xp domain.UserSkillXP) error {
	if xp.UpdatedAt == 0 {
		xp.UpdatedAt = float64(time.Now().Unix())
	}
	_, err := r.pool.Exec(ctx,
		`INSERT INTO user_skill_xp(user_id, skill_id, xp, tier, pending_verify, last_verified_at, updated_at)
		 VALUES($1, $2, $3, $4, $5, $6, $7)
		 ON CONFLICT(user_id, skill_id) DO UPDATE SET
		   xp = excluded.xp,
		   tier = excluded.tier,
		   pending_verify = excluded.pending_verify,
		   last_verified_at = excluded.last_verified_at,
		   updated_at = excluded.updated_at`,
		xp.UserID, xp.SkillID, xp.XP, xp.Tier, boolToInt(xp.PendingVerify),
		xp.LastVerifiedAt, xp.UpdatedAt)
	return err
}

func (r *PostgresRepository) InsertTaskSkillXPLog(ctx context.Context, row domain.TaskSkillXPLog) error {
	if row.LogID == "" {
		row.LogID = id.New()
	}
	if row.LoggedAt == 0 {
		row.LoggedAt = float64(time.Now().Unix())
	}
	_, err := r.pool.Exec(ctx,
		`INSERT INTO task_skill_xp_log(log_id, user_id, task_id, skill_id, xp_delta, xp_after, logged_at)
		 VALUES($1, $2, $3, $4, $5, $6, $7)`,
		row.LogID, row.UserID, row.TaskID, row.SkillID, row.XPDelta, row.XPAfter, row.LoggedAt)
	return err
}

func (r *PostgresRepository) ListTaskSkillXPLog(ctx context.Context, userID string, limit int) ([]domain.TaskSkillXPLog, error) {
	query := `SELECT log_id, user_id, task_id, skill_id, xp_delta, xp_after, logged_at
	          FROM task_skill_xp_log WHERE user_id = $1
	          ORDER BY logged_at DESC, log_id DESC`
	args := []any{userID}
	if limit > 0 {
		query += ` LIMIT $2`
		args = append(args, limit)
	}
	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanPgTaskSkillXPLogs(rows)
}

func scanPgSkill(row pgx.Row) (domain.Skill, error) {
	var (
		skill       domain.Skill
		description *string
	)
	err := row.Scan(&skill.SkillID, &skill.Language, &skill.Name, &description, &skill.Category,
		&skill.TierCount, &skill.XPPerTier, &skill.SortOrder)
	if err != nil {
		return domain.Skill{}, err
	}
	skill.Description = derefStr(description)
	return skill, nil
}

func scanPgSkills(rows pgx.Rows) ([]domain.Skill, error) {
	var out []domain.Skill
	for rows.Next() {
		var (
			skill       domain.Skill
			description *string
		)
		if err := rows.Scan(&skill.SkillID, &skill.Language, &skill.Name, &description, &skill.Category,
			&skill.TierCount, &skill.XPPerTier, &skill.SortOrder); err != nil {
			return nil, err
		}
		skill.Description = derefStr(description)
		out = append(out, skill)
	}
	return out, rows.Err()
}

func scanPgItemSkillAssociations(rows pgx.Rows) ([]domain.ItemSkillAssociation, error) {
	var out []domain.ItemSkillAssociation
	for rows.Next() {
		var row domain.ItemSkillAssociation
		if err := rows.Scan(&row.ItemID, &row.SkillID); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

func scanPgUserSkillXP(row pgx.Row) (domain.UserSkillXP, error) {
	var (
		xp            domain.UserSkillXP
		pendingVerify int
	)
	err := row.Scan(&xp.UserID, &xp.SkillID, &xp.XP, &xp.Tier, &pendingVerify, &xp.LastVerifiedAt, &xp.UpdatedAt)
	if err != nil {
		return domain.UserSkillXP{}, err
	}
	xp.PendingVerify = pendingVerify != 0
	return xp, nil
}

func scanPgUserSkillXPs(rows pgx.Rows) ([]domain.UserSkillXP, error) {
	var out []domain.UserSkillXP
	for rows.Next() {
		var (
			xp            domain.UserSkillXP
			pendingVerify int
		)
		if err := rows.Scan(&xp.UserID, &xp.SkillID, &xp.XP, &xp.Tier, &pendingVerify, &xp.LastVerifiedAt, &xp.UpdatedAt); err != nil {
			return nil, err
		}
		xp.PendingVerify = pendingVerify != 0
		out = append(out, xp)
	}
	return out, rows.Err()
}

func scanPgTaskSkillXPLogs(rows pgx.Rows) ([]domain.TaskSkillXPLog, error) {
	var out []domain.TaskSkillXPLog
	for rows.Next() {
		var row domain.TaskSkillXPLog
		if err := rows.Scan(&row.LogID, &row.UserID, &row.TaskID, &row.SkillID, &row.XPDelta, &row.XPAfter, &row.LoggedAt); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

func pgPlaceholders(start, n int) string {
	if n <= 0 {
		return ""
	}
	parts := make([]string, n)
	for i := range parts {
		parts[i] = fmt.Sprintf("$%d", start+i)
	}
	return strings.Join(parts, ",")
}
