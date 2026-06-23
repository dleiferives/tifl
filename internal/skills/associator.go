// Package skills owns language-agnostic skill-system services.
package skills

import (
	"context"
	"errors"
	"sort"

	"github.com/dleiferives/tifl/internal/db"
	"github.com/dleiferives/tifl/internal/domain"
	"github.com/dleiferives/tifl/internal/lang"
)

// ErrMissingItemID is returned when an item matches skills but has not been
// persisted yet. Association rows require a stable knowledge_items.item_id.
var ErrMissingItemID = errors.New("skills: knowledge item has no item_id")

// Associator materializes item_skill_associations from the explicit declarations
// supplied by language plugins. It is intentionally deterministic and small:
// exact canonical key matches only, no LLM classification or morphology.
type Associator struct {
	repo  db.Repository
	langs *lang.Registry
}

// NewAssociator builds a lazy item-skill association materializer.
func NewAssociator(repo db.Repository, langs *lang.Registry) *Associator {
	return &Associator{repo: repo, langs: langs}
}

// AssociateItem creates association rows for one persisted knowledge item. It is
// a no-op when the item's language has no registered skill definitions or when
// no declaration matches. Existing associations are preserved because skill-map
// migrations are not supported yet.
func (a *Associator) AssociateItem(ctx context.Context, item domain.KnowledgeItem) error {
	skillIDs := a.SkillIDsForItem(item)
	if len(skillIDs) == 0 {
		return nil
	}
	if item.ItemID == "" {
		return ErrMissingItemID
	}

	existing, err := a.repo.ListItemSkillAssociations(ctx, []string{item.ItemID})
	if err != nil {
		return err
	}
	for _, row := range existing {
		skillIDs = append(skillIDs, row.SkillID)
	}
	return a.repo.UpsertItemSkillAssociations(ctx, item.ItemID, uniqueSorted(skillIDs))
}

// EnsureAssociationsForItems loads existing knowledge items and runs association
// materialization for each. Task grading can call this as a safety fallback
// before future XP logic reads task target -> skill rows.
func (a *Associator) EnsureAssociationsForItems(ctx context.Context, itemIDs []string) error {
	for _, itemID := range uniqueSorted(itemIDs) {
		item, err := a.repo.GetKnowledgeItem(ctx, itemID)
		if err != nil {
			return err
		}
		if err := a.AssociateItem(ctx, item); err != nil {
			return err
		}
	}
	return nil
}

// SkillIDsForItem returns the language-declared skill ids that explicitly cover
// this item. It is exported for focused tests and future callers that need to
// inspect matches without writing rows.
func (a *Associator) SkillIDsForItem(item domain.KnowledgeItem) []string {
	if a == nil || a.langs == nil || item.Language == "" || item.ItemType == "" || item.Key == "" {
		return nil
	}
	l, ok := a.langs.Get(item.Language)
	if !ok {
		return nil
	}
	provider, ok := l.(lang.SkillDefinitionProvider)
	if !ok {
		return nil
	}

	var out []string
	for _, def := range provider.SkillDefinitions() {
		if def.Skill.SkillID == "" {
			continue
		}
		for _, assoc := range def.Associations {
			if assoc.ItemType != item.ItemType {
				continue
			}
			if containsString(assoc.Keys, item.Key) {
				out = append(out, def.Skill.SkillID)
				break
			}
		}
	}
	return uniqueSorted(out)
}

func containsString(values []string, needle string) bool {
	for _, value := range values {
		if value == needle {
			return true
		}
	}
	return false
}

func uniqueSorted(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}
