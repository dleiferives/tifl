package story

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/dleiferives/tifl/internal/domain"
	"github.com/dleiferives/tifl/internal/lang"
)

var ErrImportEmptyText = errors.New("story import: text is required")

type ImportRepository interface {
	CreateStory(context.Context, domain.Story) (domain.Story, error)
	ReplaceStoryTokens(context.Context, string, []domain.StoryToken) error
}

type ImportRequest struct {
	UserID   string
	Language string
	Level    string
	Title    string
	Text     string
}

// ImportText persists caller-provided target-language text as a tokenized story
// that the normal reader can load. It deliberately does not create a generation
// session or tasks; upload/PDF/EPUB extraction can layer on this path later.
func ImportText(ctx context.Context, repo ImportRepository, plugin lang.Language, req ImportRequest) (domain.Story, error) {
	text := strings.TrimSpace(req.Text)
	if text == "" {
		return domain.Story{}, ErrImportEmptyText
	}
	title := strings.TrimSpace(req.Title)
	topic := "Imported text"
	if title != "" {
		topic = "Imported: " + title
	}
	story, err := repo.CreateStory(ctx, domain.Story{
		UserID:   req.UserID,
		Language: req.Language,
		Text:     text,
		Level:    req.Level,
		Topic:    topic,
	})
	if err != nil {
		return domain.Story{}, fmt.Errorf("create imported story: %w", err)
	}
	if err := repo.ReplaceStoryTokens(ctx, story.StoryID, toStoryTokens(story.StoryID, plugin.Tokenize(text))); err != nil {
		return domain.Story{}, fmt.Errorf("tokenize imported story: %w", err)
	}
	return story, nil
}
