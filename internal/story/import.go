package story

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/dleiferives/tifl/internal/db"
	"github.com/dleiferives/tifl/internal/domain"
	"github.com/dleiferives/tifl/internal/lang"
	"github.com/dleiferives/tifl/internal/selector"
)

var ErrImportEmptyText = errors.New("story import: text is required")

type ImportRepository interface {
	Tx(context.Context, func(db.Repository) error) error
	ListKnowledgeItems(context.Context, string) ([]domain.KnowledgeItem, error)
	CreateSession(context.Context, domain.Session) (domain.Session, error)
	CreateStory(context.Context, domain.Story) (domain.Story, error)
	ReplaceStoryTokens(context.Context, string, []domain.StoryToken) error
	SetSessionSelection(context.Context, string, string, []string, []string) error
	UpsertKnowledgeItem(context.Context, domain.KnowledgeItem) (string, error)
	UpsertStage(context.Context, domain.GenerationStage) error
}

type ImportRequest struct {
	UserID   string
	Language string
	Level    string
	Title    string
	Text     string
}

// ImportResult is the first-class study session and story created from
// caller-provided text.
type ImportResult struct {
	Session domain.Session
	Story   domain.Story
}

// ImportText persists caller-provided target-language text as a first-class,
// session-backed story. The imported content and tokenization stages are marked
// complete so task generation can later resume at the task stages without ever
// replacing the user's text.
func ImportText(ctx context.Context, repo ImportRepository, plugin lang.Language, req ImportRequest) (ImportResult, error) {
	text := strings.TrimSpace(req.Text)
	if text == "" {
		return ImportResult{}, ErrImportEmptyText
	}
	title := strings.TrimSpace(req.Title)
	topic := "Imported text"
	if title != "" {
		topic = "Imported: " + title
	}
	tokens := plugin.Tokenize(text)
	targets, err := importedStoryTargets(ctx, repo, req.Language, req.Level, tokens)
	if err != nil {
		return ImportResult{}, fmt.Errorf("select imported story targets: %w", err)
	}

	var result ImportResult
	err = repo.Tx(ctx, func(tx db.Repository) error {
		session, err := tx.CreateSession(ctx, domain.Session{
			UserID:      req.UserID,
			Language:    req.Language,
			Level:       req.Level,
			SessionType: domain.SessionUserAdded,
			Topic:       topic,
			Status:      domain.StatusReady,
		})
		if err != nil {
			return fmt.Errorf("create user-added session: %w", err)
		}
		story, err := tx.CreateStory(ctx, domain.Story{
			UserID:    req.UserID,
			Language:  req.Language,
			Text:      text,
			Level:     req.Level,
			Topic:     topic,
			SessionID: &session.SessionID,
		})
		if err != nil {
			return fmt.Errorf("create user-added story: %w", err)
		}
		if err := tx.ReplaceStoryTokens(ctx, story.StoryID, toStoryTokens(story.StoryID, tokens)); err != nil {
			return fmt.Errorf("tokenize user-added story: %w", err)
		}

		targetIDs := make([]string, 0, len(targets))
		for _, target := range targets {
			if target.ItemID == "" {
				target.ItemID, err = tx.UpsertKnowledgeItem(ctx, target)
				if err != nil {
					return fmt.Errorf("persist imported story target %q: %w", target.Key, err)
				}
			}
			targetIDs = append(targetIDs, target.ItemID)
		}
		if err := tx.SetSessionSelection(ctx, session.SessionID, story.StoryID, targetIDs, nil); err != nil {
			return fmt.Errorf("link user-added story: %w", err)
		}
		completedAt := story.GeneratedAt
		for _, stage := range []string{domain.StageStoryImport, domain.StageTokenization} {
			if err := tx.UpsertStage(ctx, domain.GenerationStage{
				SessionID: session.SessionID, Stage: stage, Status: domain.StageComplete,
				StartedAt: &completedAt, CompletedAt: &completedAt,
			}); err != nil {
				return fmt.Errorf("mark %s complete: %w", stage, err)
			}
		}

		session.StoryID = &story.StoryID
		session.SelectedTargets = targetIDs
		result = ImportResult{Session: session, Story: story}
		return nil
	})
	if err != nil {
		return ImportResult{}, err
	}
	return result, nil
}

// importedStoryTargets gives task generation a bounded set of real knowledge
// item ids drawn from the text. Existing catalogue rows are reused without
// overwriting their metadata; previously unseen keys are created only when they
// fit within the session's normal target budget.
func importedStoryTargets(ctx context.Context, repo ImportRepository, language, level string, tokens []lang.Token) ([]domain.KnowledgeItem, error) {
	existing, err := repo.ListKnowledgeItems(ctx, language)
	if err != nil {
		return nil, err
	}
	byKey := make(map[string]domain.KnowledgeItem, len(existing))
	for _, item := range existing {
		if item.ItemType == "word" {
			byKey[item.Key] = item
		}
	}

	limit := selector.BudgetForLevel(level).TargetCount
	seen := make(map[string]bool, limit)
	out := make([]domain.KnowledgeItem, 0, limit)
	for _, token := range tokens {
		if len(out) >= limit {
			break
		}
		if !token.IsWord || token.Key == "" || seen[token.Key] {
			continue
		}
		seen[token.Key] = true
		if item, ok := byKey[token.Key]; ok {
			out = append(out, item)
			continue
		}
		out = append(out, domain.KnowledgeItem{
			Language: language,
			ItemType: "word",
			Key:      token.Key,
			Metadata: map[string]any{"display": token.Surface},
		})
	}
	return out, nil
}
