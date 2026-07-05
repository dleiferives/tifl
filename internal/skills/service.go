package skills

import (
	"context"
	"sort"

	"github.com/dleiferives/tifl/internal/db"
	"github.com/dleiferives/tifl/internal/domain"
	"github.com/dleiferives/tifl/internal/id"
	"github.com/dleiferives/tifl/internal/tasks"
)

// XPService is the persistence boundary for skill XP. It keeps handlers thin:
// handlers provide the accepted task signal, while this service resolves item
// associations, calls the pure engine, and writes XP rows plus audit logs.
type XPService struct {
	repo       Store
	associator *Associator
	engine     *XPEngine
}

func NewXPService(repo Store, associator *Associator, engine *XPEngine) *XPService {
	if engine == nil {
		engine = NewXPEngine(DefaultXPConfig())
	}
	return &XPService{repo: repo, associator: associator, engine: engine}
}

// ApplyTaskSignal persists skill-XP effects for one accepted grade. Missing
// user_skill_xp rows are treated as tier 0 / 0 XP and are only materialized when
// the engine returns an actual XP delta.
func (s *XPService) ApplyTaskSignal(ctx context.Context, userID string, task domain.Task, signal tasks.LearningSignal, now float64) ([]XPChange, error) {
	if s == nil || len(signal.TargetItemIDs) == 0 {
		return nil, nil
	}
	targetItemIDs := uniqueSorted(signal.TargetItemIDs)
	if s.associator != nil {
		if err := s.associator.EnsureAssociationsForItems(ctx, targetItemIDs); err != nil {
			return nil, err
		}
	}

	assocs, err := s.repo.ListItemSkillAssociations(ctx, targetItemIDs)
	if err != nil {
		return nil, err
	}
	if len(assocs) == 0 {
		return nil, nil
	}

	targetSkills := make(map[string]bool)
	demonstratedSkills := make(map[string]bool)
	for _, assoc := range assocs {
		targetSkills[assoc.SkillID] = true
		if signal.Demonstrated(assoc.ItemID) {
			demonstratedSkills[assoc.SkillID] = true
		}
	}
	targetSkillIDs := sortedSkillIDs(targetSkills)
	if len(targetSkillIDs) == 0 {
		return nil, nil
	}

	skillsByID, err := s.loadSkills(ctx, targetSkillIDs)
	if err != nil {
		return nil, err
	}
	current, err := s.loadCurrent(ctx, userID, targetSkillIDs)
	if err != nil {
		return nil, err
	}

	changes, err := s.engine.Apply(XPInput{
		TaskType:             task.TaskType,
		OverallCorrect:       signal.OverallCorrect,
		TargetSkillIDs:       targetSkillIDs,
		DemonstratedSkillIDs: sortedSkillIDs(demonstratedSkills),
		Current:              current,
		Skills:               skillsByID,
	})
	if err != nil {
		return nil, err
	}

	// All XP state rows and their audit log lines commit atomically: a partial
	// write cannot leave a skill's XP updated without its log entry (or leave
	// half the affected skills updated).
	if err := s.repo.Tx(ctx, func(repo db.Repository) error {
		for i := range changes {
			state := changes[i].State
			state.UserID = userID
			state.SkillID = changes[i].SkillID
			state.UpdatedAt = now
			changes[i].State = state
			if err := repo.UpsertUserSkillXP(ctx, state); err != nil {
				return err
			}
			if err := repo.InsertTaskSkillXPLog(ctx, domain.TaskSkillXPLog{
				LogID:    id.New(),
				UserID:   userID,
				TaskID:   task.TaskID,
				SkillID:  changes[i].SkillID,
				XPDelta:  changes[i].XPDelta,
				XPAfter:  changes[i].XPAfter,
				LoggedAt: now,
			}); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return nil, err
	}
	return changes, nil
}

func (s *XPService) loadSkills(ctx context.Context, skillIDs []string) (map[string]domain.Skill, error) {
	out := make(map[string]domain.Skill, len(skillIDs))
	for _, skillID := range skillIDs {
		skill, err := s.repo.GetSkill(ctx, skillID)
		if err != nil {
			return nil, err
		}
		out[skillID] = skill
	}
	return out, nil
}

func (s *XPService) loadCurrent(ctx context.Context, userID string, skillIDs []string) (map[string]domain.UserSkillXP, error) {
	rows, err := s.repo.ListUserSkillXP(ctx, userID, skillIDs)
	if err != nil {
		return nil, err
	}
	out := make(map[string]domain.UserSkillXP, len(rows))
	for _, row := range rows {
		out[row.SkillID] = row
	}
	return out, nil
}

func sortedSkillIDs(values map[string]bool) []string {
	out := make([]string, 0, len(values))
	for value := range values {
		if value != "" {
			out = append(out, value)
		}
	}
	sort.Strings(out)
	return out
}
