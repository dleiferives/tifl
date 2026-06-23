package skills

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/dleiferives/tifl/internal/db"
	"github.com/dleiferives/tifl/internal/domain"
	"github.com/dleiferives/tifl/internal/llm"
)

const maxVerificationEvidence = 10

// VerificationService confirms or rejects pending skill-tier promotions. It runs
// off the critical path as a background goroutine after a task submission crosses
// a tier threshold. When no LLM client is wired (local / no-gateway mode) it
// auto-approves all pending rows — the deterministic XP system is fully usable
// without a gateway, and verification becomes a safeguard added in cloud mode.
type VerificationService struct {
	repo   db.Repository
	client llm.Client // nil → auto-approve
}

// NewVerificationService builds a tier-verification runner. Passing a nil client
// enables the auto-approve path so the system works locally without a gateway.
func NewVerificationService(repo db.Repository, client llm.Client) *VerificationService {
	return &VerificationService{repo: repo, client: client}
}

// VerifySkill resolves the pending_verify flag for one (user, skill) pair. It
// is a no-op when no pending row exists. Promote clears pending_verify and
// records last_verified_at. Reject steps XP back just below the tier threshold
// so the user must re-accumulate before verification is attempted again.
func (s *VerificationService) VerifySkill(ctx context.Context, userID, skillID string) error {
	xp, err := s.repo.GetUserSkillXP(ctx, userID, skillID)
	if err == db.ErrNotFound {
		return nil
	}
	if err != nil {
		return fmt.Errorf("skills: load XP for verification (%s, %s): %w", userID, skillID, err)
	}
	if !xp.PendingVerify {
		return nil
	}

	skill, err := s.repo.GetSkill(ctx, skillID)
	if err != nil {
		return fmt.Errorf("skills: load skill for verification %s: %w", skillID, err)
	}

	promote := true // default: auto-approve when no LLM client is configured
	if s.client != nil {
		promote, err = s.runLLMVerification(ctx, userID, xp, skill)
		if err != nil {
			return err
		}
	}

	now := float64(time.Now().Unix())
	if promote {
		xp.PendingVerify = false
		xp.LastVerifiedAt = &now
	} else {
		// Step XP just below the threshold so the tier drops back and the user
		// must earn more evidence before another verification is queued.
		xpPerTier := maxInt(skill.XPPerTier, 1)
		xp.XP = maxInt(xp.Tier*xpPerTier-1, 0)
		xp.Tier = xp.XP / xpPerTier
		xp.PendingVerify = false
	}
	xp.UpdatedAt = now
	return s.repo.UpsertUserSkillXP(ctx, xp)
}

func (s *VerificationService) runLLMVerification(ctx context.Context, userID string, xp domain.UserSkillXP, skill domain.Skill) (bool, error) {
	evidence, err := s.gatherEvidence(ctx, userID, xp.SkillID)
	if err != nil {
		return false, fmt.Errorf("skills: gather evidence for %s: %w", xp.SkillID, err)
	}

	builder := llm.SkillTierVerifierBuilder{
		Skill:       skill,
		CurrentTier: maxInt(xp.Tier-1, 0),
		TargetTier:  xp.Tier,
		Evidence:    evidence,
	}
	lc := domain.LearnerCtx{UserID: userID, Language: skill.Language}

	result, err := llm.CompleteJSON[llm.SkillTierVerificationResult](
		ctx, s.client, builder, lc,
		func(r llm.SkillTierVerificationResult) error { return r.Validate() },
	)
	if err != nil {
		return false, fmt.Errorf("skills: LLM tier verification for %s: %w", xp.SkillID, err)
	}
	return result.Decision == llm.SkillTierDecisionPromote, nil
}

// gatherEvidence pulls recent task grades involving the skill from the XP log
// and builds structured evidence samples for the LLM verifier. Evidence gaps
// (e.g. tasks deleted or not yet graded) are skipped rather than propagated.
func (s *VerificationService) gatherEvidence(ctx context.Context, userID, skillID string) ([]llm.SkillTierEvidence, error) {
	logs, err := s.repo.ListTaskSkillXPLog(ctx, userID, 50)
	if err != nil {
		return nil, err
	}

	seen := make(map[string]bool)
	var taskIDs []string
	for _, log := range logs {
		if log.SkillID != skillID || seen[log.TaskID] {
			continue
		}
		seen[log.TaskID] = true
		taskIDs = append(taskIDs, log.TaskID)
		if len(taskIDs) >= maxVerificationEvidence {
			break
		}
	}

	var out []llm.SkillTierEvidence
	for _, taskID := range taskIDs {
		task, err := s.repo.GetTask(ctx, userID, taskID)
		if err != nil || task.GradedAt == nil {
			continue
		}
		ev := llm.SkillTierEvidence{
			TaskType:        task.TaskType,
			OccurredAt:      task.GradedAt,
			PromptSummary:   truncateJSON(task.Content, 200),
			ResponseSummary: truncateJSON(task.Response, 200),
		}
		if correct, ok := task.Grade["correct"].(bool); ok {
			ev.Correct = correct
		}
		if score, ok := task.Grade["score"].(float64); ok {
			ev.Score = score
		}
		if items, ok := task.Grade["items_demonstrated"].([]any); ok {
			for _, item := range items {
				if s, ok := item.(string); ok {
					ev.ItemsDemonstrated = append(ev.ItemsDemonstrated, s)
				}
			}
		}
		out = append(out, ev)
	}
	return out, nil
}

func truncateJSON(m map[string]any, max int) string {
	if len(m) == 0 {
		return ""
	}
	b, err := json.Marshal(m)
	if err != nil {
		return ""
	}
	s := string(b)
	if len(s) > max {
		return s[:max]
	}
	return s
}
