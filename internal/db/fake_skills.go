package db

import (
	"context"
	"sort"
	"time"

	"github.com/dleiferives/tifl/internal/domain"
	"github.com/dleiferives/tifl/internal/id"
)

func (r *FakeRepository) UpsertSkill(_ context.Context, skill domain.Skill) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.languages[skill.Language]; !ok {
		return errFakeFK("skills.language")
	}
	r.skills[skill.SkillID] = cloneSkill(skill)
	return nil
}

func (r *FakeRepository) ListSkills(_ context.Context, language string) ([]domain.Skill, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []domain.Skill
	for _, skill := range r.skills {
		if skill.Language == language {
			out = append(out, cloneSkill(skill))
		}
	}
	sortSkills(out)
	return out, nil
}

func (r *FakeRepository) GetSkill(_ context.Context, skillID string) (domain.Skill, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	skill, ok := r.skills[skillID]
	if !ok {
		return domain.Skill{}, ErrNotFound
	}
	return cloneSkill(skill), nil
}

func (r *FakeRepository) UpsertItemSkillAssociations(_ context.Context, itemID string, skillIDs []string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.items[itemID]; !ok {
		return errFakeFK("item_skill_associations.item_id")
	}
	set := make(map[string]bool)
	for _, skillID := range uniqueStrings(skillIDs) {
		if _, ok := r.skills[skillID]; !ok {
			return errFakeFK("item_skill_associations.skill_id")
		}
		set[skillID] = true
	}
	r.itemSkills[itemID] = set
	return nil
}

func (r *FakeRepository) ListItemSkillAssociations(_ context.Context, itemIDs []string) ([]domain.ItemSkillAssociation, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []domain.ItemSkillAssociation
	for _, itemID := range uniqueStrings(itemIDs) {
		for skillID := range r.itemSkills[itemID] {
			out = append(out, domain.ItemSkillAssociation{ItemID: itemID, SkillID: skillID})
		}
	}
	sortAssociations(out)
	return out, nil
}

func (r *FakeRepository) ListSkillAssociations(_ context.Context, skillID string) ([]domain.ItemSkillAssociation, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []domain.ItemSkillAssociation
	for itemID, skillIDs := range r.itemSkills {
		if skillIDs[skillID] {
			out = append(out, domain.ItemSkillAssociation{ItemID: itemID, SkillID: skillID})
		}
	}
	sortAssociations(out)
	return out, nil
}

func (r *FakeRepository) GetUserSkillXP(_ context.Context, userID, skillID string) (domain.UserSkillXP, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	xp, ok := r.userSkills[userID+"\x00"+skillID]
	if !ok {
		return domain.UserSkillXP{}, ErrNotFound
	}
	return cloneUserSkillXP(xp), nil
}

func (r *FakeRepository) ListUserSkillXP(_ context.Context, userID string, skillIDs []string) ([]domain.UserSkillXP, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	filter := make(map[string]bool)
	for _, skillID := range uniqueStrings(skillIDs) {
		filter[skillID] = true
	}
	var out []domain.UserSkillXP
	for _, xp := range r.userSkills {
		if xp.UserID != userID {
			continue
		}
		if len(filter) > 0 && !filter[xp.SkillID] {
			continue
		}
		out = append(out, cloneUserSkillXP(xp))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].SkillID < out[j].SkillID })
	return out, nil
}

func (r *FakeRepository) UpsertUserSkillXP(_ context.Context, xp domain.UserSkillXP) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.users[xp.UserID]; !ok {
		return errFakeFK("user_skill_xp.user_id")
	}
	if _, ok := r.skills[xp.SkillID]; !ok {
		return errFakeFK("user_skill_xp.skill_id")
	}
	if xp.UpdatedAt == 0 {
		xp.UpdatedAt = float64(time.Now().Unix())
	}
	r.userSkills[xp.UserID+"\x00"+xp.SkillID] = cloneUserSkillXP(xp)
	return nil
}

func (r *FakeRepository) ListSkillProgress(_ context.Context, userID, language string) ([]domain.SkillProgress, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []domain.SkillProgress
	for _, skill := range r.skills {
		if skill.Language != language {
			continue
		}
		progress := domain.SkillProgress{Skill: cloneSkill(skill)}
		if xp, ok := r.userSkills[userID+"\x00"+skill.SkillID]; ok {
			progress.XP = xp.XP
			progress.Tier = xp.Tier
			progress.PendingVerify = xp.PendingVerify
			progress.LastVerifiedAt = cloneFloat(xp.LastVerifiedAt)
			progress.UpdatedAt = cloneFloat(&xp.UpdatedAt)
		}
		out = append(out, progress)
	}
	sortSkillProgress(out)
	return out, nil
}

func (r *FakeRepository) InsertTaskSkillXPLog(_ context.Context, row domain.TaskSkillXPLog) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.users[row.UserID]; !ok {
		return errFakeFK("task_skill_xp_log.user_id")
	}
	task, ok := r.tasks[row.TaskID]
	if !ok || task.UserID != row.UserID {
		return errFakeFK("task_skill_xp_log.task_id")
	}
	if _, ok := r.skills[row.SkillID]; !ok {
		return errFakeFK("task_skill_xp_log.skill_id")
	}
	if row.LogID == "" {
		row.LogID = id.New()
	}
	for _, existing := range r.skillLogs {
		if existing.LogID == row.LogID {
			return errFakeUnique("task_skill_xp_log.log_id")
		}
	}
	if row.LoggedAt == 0 {
		row.LoggedAt = float64(time.Now().Unix())
	}
	r.skillLogs = append(r.skillLogs, row)
	return nil
}

func (r *FakeRepository) ListTaskSkillXPLog(_ context.Context, userID string, limit int) ([]domain.TaskSkillXPLog, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []domain.TaskSkillXPLog
	for _, row := range r.skillLogs {
		if row.UserID == userID {
			out = append(out, row)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].LoggedAt != out[j].LoggedAt {
			return out[i].LoggedAt > out[j].LoggedAt
		}
		return out[i].LogID > out[j].LogID
	})
	if limit > 0 && limit < len(out) {
		out = out[:limit]
	}
	return out, nil
}

func cloneSkill(skill domain.Skill) domain.Skill {
	skill.SortOrder = cloneInt(skill.SortOrder)
	return skill
}

func cloneUserSkillXP(xp domain.UserSkillXP) domain.UserSkillXP {
	xp.LastVerifiedAt = cloneFloat(xp.LastVerifiedAt)
	return xp
}

func sortSkills(skills []domain.Skill) {
	sort.Slice(skills, func(i, j int) bool {
		return skillLess(skills[i], skills[j])
	})
}

func sortSkillProgress(rows []domain.SkillProgress) {
	sort.Slice(rows, func(i, j int) bool {
		return skillLess(rows[i].Skill, rows[j].Skill)
	})
}

func skillLess(a, b domain.Skill) bool {
	if a.Category != b.Category {
		return a.Category < b.Category
	}
	if (a.SortOrder == nil) != (b.SortOrder == nil) {
		return a.SortOrder != nil
	}
	if a.SortOrder != nil && *a.SortOrder != *b.SortOrder {
		return *a.SortOrder < *b.SortOrder
	}
	if a.Name != b.Name {
		return a.Name < b.Name
	}
	return a.SkillID < b.SkillID
}

func sortAssociations(rows []domain.ItemSkillAssociation) {
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].ItemID != rows[j].ItemID {
			return rows[i].ItemID < rows[j].ItemID
		}
		return rows[i].SkillID < rows[j].SkillID
	})
}
