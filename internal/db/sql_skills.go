package db

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/dleiferives/tifl/internal/domain"
	"github.com/dleiferives/tifl/internal/id"
)

func (r *SQLRepository) UpsertSkill(ctx context.Context, skill domain.Skill) error {
	_, err := r.exec(ctx,
		`INSERT INTO skills(skill_id, language, name, description, category, tier_count, xp_per_tier, sort_order)
		 VALUES(?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(skill_id) DO UPDATE SET
		   language = excluded.language,
		   name = excluded.name,
		   description = excluded.description,
		   category = excluded.category,
		   tier_count = excluded.tier_count,
		   xp_per_tier = excluded.xp_per_tier,
		   sort_order = excluded.sort_order`,
		skill.SkillID, skill.Language, skill.Name, emptyToNull(skill.Description), skill.Category,
		skill.TierCount, skill.XPPerTier, nullInt(skill.SortOrder))
	return err
}

func (r *SQLRepository) ListSkills(ctx context.Context, language string) ([]domain.Skill, error) {
	rows, err := r.query(ctx,
		`SELECT skill_id, language, name, description, category, tier_count, xp_per_tier, sort_order
		 FROM skills WHERE language = ?
		 ORDER BY category, sort_order IS NULL, sort_order, name, skill_id`, language)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanSQLiteSkills(rows)
}

func (r *SQLRepository) GetSkill(ctx context.Context, skillID string) (domain.Skill, error) {
	skill, err := scanSQLiteSkill(r.queryRow(ctx,
		`SELECT skill_id, language, name, description, category, tier_count, xp_per_tier, sort_order
		 FROM skills WHERE skill_id = ?`, skillID))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Skill{}, ErrNotFound
	}
	return skill, err
}

func (r *SQLRepository) UpsertItemSkillAssociations(ctx context.Context, itemID string, skillIDs []string) error {
	tx, err := r.begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.exec(ctx, `DELETE FROM item_skill_associations WHERE item_id = ?`, itemID); err != nil {
		return err
	}
	for _, skillID := range uniqueStrings(skillIDs) {
		if _, err := tx.exec(ctx,
			`INSERT INTO item_skill_associations(item_id, skill_id) VALUES(?, ?)`,
			itemID, skillID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (r *SQLRepository) ListItemSkillAssociations(ctx context.Context, itemIDs []string) ([]domain.ItemSkillAssociation, error) {
	itemIDs = uniqueStrings(itemIDs)
	if len(itemIDs) == 0 {
		return nil, nil
	}
	args := make([]any, len(itemIDs))
	for i, itemID := range itemIDs {
		args[i] = itemID
	}
	rows, err := r.query(ctx,
		`SELECT item_id, skill_id FROM item_skill_associations
		 WHERE item_id IN (`+sqlitePlaceholders(len(itemIDs))+`)
		 ORDER BY item_id, skill_id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanSQLiteItemSkillAssociations(rows)
}

func (r *SQLRepository) ListSkillAssociations(ctx context.Context, skillID string) ([]domain.ItemSkillAssociation, error) {
	rows, err := r.query(ctx,
		`SELECT item_id, skill_id FROM item_skill_associations
		 WHERE skill_id = ? ORDER BY item_id`, skillID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanSQLiteItemSkillAssociations(rows)
}

func (r *SQLRepository) GetUserSkillXP(ctx context.Context, userID, skillID string) (domain.UserSkillXP, error) {
	xp, err := scanSQLiteUserSkillXP(r.queryRow(ctx,
		`SELECT user_id, skill_id, xp, tier, pending_verify, last_verified_at, updated_at
		 FROM user_skill_xp WHERE user_id = ? AND skill_id = ?`, userID, skillID))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.UserSkillXP{}, ErrNotFound
	}
	return xp, err
}

func (r *SQLRepository) ListUserSkillXP(ctx context.Context, userID string, skillIDs []string) ([]domain.UserSkillXP, error) {
	skillIDs = uniqueStrings(skillIDs)
	query := `SELECT user_id, skill_id, xp, tier, pending_verify, last_verified_at, updated_at
	          FROM user_skill_xp WHERE user_id = ?`
	args := []any{userID}
	if len(skillIDs) > 0 {
		for _, skillID := range skillIDs {
			args = append(args, skillID)
		}
		query += ` AND skill_id IN (` + sqlitePlaceholders(len(skillIDs)) + `)`
	}
	query += ` ORDER BY skill_id`
	rows, err := r.query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanSQLiteUserSkillXPs(rows)
}

func (r *SQLRepository) UpsertUserSkillXP(ctx context.Context, xp domain.UserSkillXP) error {
	if xp.UpdatedAt == 0 {
		xp.UpdatedAt = float64(time.Now().Unix())
	}
	_, err := r.exec(ctx,
		`INSERT INTO user_skill_xp(user_id, skill_id, xp, tier, pending_verify, last_verified_at, updated_at)
		 VALUES(?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(user_id, skill_id) DO UPDATE SET
		   xp = excluded.xp,
		   tier = excluded.tier,
		   pending_verify = excluded.pending_verify,
		   last_verified_at = excluded.last_verified_at,
		   updated_at = excluded.updated_at`,
		xp.UserID, xp.SkillID, xp.XP, xp.Tier, boolToInt(xp.PendingVerify),
		nullFloat(xp.LastVerifiedAt), xp.UpdatedAt)
	return err
}

func (r *SQLRepository) ListSkillProgress(ctx context.Context, userID, language string) ([]domain.SkillProgress, error) {
	rows, err := r.query(ctx,
		`SELECT s.skill_id, s.language, s.name, s.description, s.category,
		        s.tier_count, s.xp_per_tier, s.sort_order,
		        COALESCE(ux.xp, 0), COALESCE(ux.tier, 0), COALESCE(ux.pending_verify, 0),
		        ux.last_verified_at, ux.updated_at
		 FROM skills s
		 LEFT JOIN user_skill_xp ux
		   ON ux.skill_id = s.skill_id AND ux.user_id = ?
		 WHERE s.language = ?
		 ORDER BY s.category, s.sort_order IS NULL, s.sort_order, s.name, s.skill_id`, userID, language)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []domain.SkillProgress
	for rows.Next() {
		var (
			p                         domain.SkillProgress
			description               sql.NullString
			sortOrder                 sql.NullInt64
			pending                   int
			lastVerifiedAt, updatedAt sql.NullFloat64
		)
		if err := rows.Scan(&p.SkillID, &p.Language, &p.Name, &description, &p.Category,
			&p.TierCount, &p.XPPerTier, &sortOrder,
			&p.XP, &p.Tier, &pending, &lastVerifiedAt, &updatedAt); err != nil {
			return nil, err
		}
		p.Description = description.String
		if sortOrder.Valid {
			v := int(sortOrder.Int64)
			p.SortOrder = &v
		}
		p.PendingVerify = pending != 0
		p.LastVerifiedAt = floatPtr(lastVerifiedAt)
		p.UpdatedAt = floatPtr(updatedAt)
		out = append(out, p)
	}
	return out, rows.Err()
}

func (r *SQLRepository) InsertTaskSkillXPLog(ctx context.Context, row domain.TaskSkillXPLog) error {
	if row.LogID == "" {
		row.LogID = id.New()
	}
	if row.LoggedAt == 0 {
		row.LoggedAt = float64(time.Now().Unix())
	}
	_, err := r.exec(ctx,
		`INSERT INTO task_skill_xp_log(log_id, user_id, task_id, skill_id, xp_delta, xp_after, logged_at)
		 VALUES(?, ?, ?, ?, ?, ?, ?)`,
		row.LogID, row.UserID, row.TaskID, row.SkillID, row.XPDelta, row.XPAfter, row.LoggedAt)
	return err
}

func (r *SQLRepository) ListTaskSkillXPLog(ctx context.Context, userID string, limit int) ([]domain.TaskSkillXPLog, error) {
	query := `SELECT log_id, user_id, task_id, skill_id, xp_delta, xp_after, logged_at
	          FROM task_skill_xp_log WHERE user_id = ?
	          ORDER BY logged_at DESC, log_id DESC`
	args := []any{userID}
	if limit > 0 {
		query += ` LIMIT ?`
		args = append(args, limit)
	}
	rows, err := r.query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanSQLiteTaskSkillXPLogs(rows)
}

func scanSQLiteSkill(row *sql.Row) (domain.Skill, error) {
	var (
		skill       domain.Skill
		description sql.NullString
		sortOrder   sql.NullInt64
	)
	err := row.Scan(&skill.SkillID, &skill.Language, &skill.Name, &description, &skill.Category,
		&skill.TierCount, &skill.XPPerTier, &sortOrder)
	if err != nil {
		return domain.Skill{}, err
	}
	skill.Description = description.String
	if sortOrder.Valid {
		v := int(sortOrder.Int64)
		skill.SortOrder = &v
	}
	return skill, nil
}

func scanSQLiteSkills(rows *sql.Rows) ([]domain.Skill, error) {
	var out []domain.Skill
	for rows.Next() {
		var (
			skill       domain.Skill
			description sql.NullString
			sortOrder   sql.NullInt64
		)
		if err := rows.Scan(&skill.SkillID, &skill.Language, &skill.Name, &description, &skill.Category,
			&skill.TierCount, &skill.XPPerTier, &sortOrder); err != nil {
			return nil, err
		}
		skill.Description = description.String
		if sortOrder.Valid {
			v := int(sortOrder.Int64)
			skill.SortOrder = &v
		}
		out = append(out, skill)
	}
	return out, rows.Err()
}

func scanSQLiteItemSkillAssociations(rows *sql.Rows) ([]domain.ItemSkillAssociation, error) {
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

func scanSQLiteUserSkillXP(row *sql.Row) (domain.UserSkillXP, error) {
	var (
		xp             domain.UserSkillXP
		pendingVerify  int
		lastVerifiedAt sql.NullFloat64
	)
	err := row.Scan(&xp.UserID, &xp.SkillID, &xp.XP, &xp.Tier, &pendingVerify, &lastVerifiedAt, &xp.UpdatedAt)
	if err != nil {
		return domain.UserSkillXP{}, err
	}
	xp.PendingVerify = pendingVerify != 0
	xp.LastVerifiedAt = floatPtr(lastVerifiedAt)
	return xp, nil
}

func scanSQLiteUserSkillXPs(rows *sql.Rows) ([]domain.UserSkillXP, error) {
	var out []domain.UserSkillXP
	for rows.Next() {
		var (
			xp             domain.UserSkillXP
			pendingVerify  int
			lastVerifiedAt sql.NullFloat64
		)
		if err := rows.Scan(&xp.UserID, &xp.SkillID, &xp.XP, &xp.Tier, &pendingVerify, &lastVerifiedAt, &xp.UpdatedAt); err != nil {
			return nil, err
		}
		xp.PendingVerify = pendingVerify != 0
		xp.LastVerifiedAt = floatPtr(lastVerifiedAt)
		out = append(out, xp)
	}
	return out, rows.Err()
}

func scanSQLiteTaskSkillXPLogs(rows *sql.Rows) ([]domain.TaskSkillXPLog, error) {
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

func sqlitePlaceholders(n int) string {
	if n <= 0 {
		return ""
	}
	return strings.TrimRight(strings.Repeat("?,", n), ",")
}

func uniqueStrings(values []string) []string {
	seen := make(map[string]bool, len(values))
	out := make([]string, 0, len(values))
	for _, v := range values {
		if v == "" || seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	return out
}
