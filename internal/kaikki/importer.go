// Package kaikki imports Wiktextract/kaikki.org JSONL dictionary data into the
// shared reader definition cache.
package kaikki

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"
	"unicode"

	"github.com/dleiferives/tifl/internal/domain"
	"github.com/dleiferives/tifl/internal/id"
	"github.com/dleiferives/tifl/internal/lang"
)

const maxJSONLLine = 32 * 1024 * 1024

// Store is the repository surface the importer needs.
type Store interface {
	UpsertDefinition(ctx context.Context, d domain.Definition) error
	UpsertDefinitionImport(ctx context.Context, imp domain.DefinitionImport) error
}

type Options struct {
	Language       lang.Language
	SourcePath     string
	DatasetVersion string
	Now            func() float64
}

type Stats struct {
	ImportID           string
	EntriesRead        int
	EntriesMatched     int
	DefinitionsWritten int
	FormDefinitions    int
}

type Importer struct {
	store Store
	opts  Options
}

func NewImporter(store Store, opts Options) (*Importer, error) {
	if store == nil {
		return nil, fmt.Errorf("kaikki: store is required")
	}
	if opts.Language == nil {
		return nil, fmt.Errorf("kaikki: language is required")
	}
	if opts.Now == nil {
		opts.Now = func() float64 { return float64(time.Now().Unix()) }
	}
	return &Importer{store: store, opts: opts}, nil
}

func (i *Importer) Import(ctx context.Context, r io.Reader) (stats Stats, err error) {
	started := i.opts.Now()
	run := domain.DefinitionImport{
		ImportID:       id.New(),
		Language:       i.opts.Language.Code(),
		Source:         domain.DefinitionImportSourceKaikki,
		SourcePath:     i.opts.SourcePath,
		DatasetVersion: i.opts.DatasetVersion,
		StartedAt:      started,
		Status:         domain.DefinitionImportRunning,
	}
	if run.SourcePath == "" {
		run.SourcePath = "<stream>"
	}
	if err := i.store.UpsertDefinitionImport(ctx, run); err != nil {
		return Stats{}, err
	}

	stats = Stats{ImportID: run.ImportID}
	defs := make(map[string]domain.Definition)
	defer func() {
		completed := i.opts.Now()
		run.CompletedAt = &completed
		run.EntriesRead = stats.EntriesRead
		run.EntriesMatched = stats.EntriesMatched
		run.DefinitionsWritten = stats.DefinitionsWritten
		if err != nil {
			run.Status = domain.DefinitionImportFailed
			run.Error = err.Error()
		} else {
			run.Status = domain.DefinitionImportComplete
		}
		if recordErr := i.store.UpsertDefinitionImport(context.Background(), run); err == nil && recordErr != nil {
			err = recordErr
		}
	}()

	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 1024*1024), maxJSONLLine)
	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return stats, err
		}
		stats.EntriesRead++
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var entry wiktextractEntry
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			return stats, fmt.Errorf("kaikki: parse line %d: %w", stats.EntriesRead, err)
		}
		if entry.LangCode != i.opts.Language.Code() {
			continue
		}
		base, ok, err := i.definitionForEntry(entry, started)
		if err != nil {
			return stats, fmt.Errorf("kaikki: entry %q line %d: %w", entry.Word, stats.EntriesRead, err)
		}
		if !ok {
			continue
		}
		stats.EntriesMatched++
		if err := i.addDefinition(ctx, defs, base); err != nil {
			return stats, err
		}
		stats.DefinitionsWritten = len(defs)

		canonicalKey := base.ItemKey
		for _, f := range entry.Forms {
			if !usableForm(f) {
				continue
			}
			key, err := i.opts.Language.ResolveKey(f.Form)
			if err != nil {
				return stats, fmt.Errorf("kaikki: resolve form %q for %q: %w", f.Form, entry.Word, err)
			}
			if key == "" || key == canonicalKey {
				continue
			}
			alias := base
			alias.ItemKey = key
			if err := i.addDefinition(ctx, defs, alias); err != nil {
				return stats, err
			}
			stats.FormDefinitions++
			stats.DefinitionsWritten = len(defs)
		}
	}
	if err := scanner.Err(); err != nil {
		return stats, fmt.Errorf("kaikki: scan JSONL: %w", err)
	}
	return stats, nil
}

func (i *Importer) addDefinition(ctx context.Context, defs map[string]domain.Definition, d domain.Definition) error {
	if existing, ok := defs[d.ItemKey]; ok {
		merged := mergeDefinitions(existing, d)
		if merged == existing {
			return nil
		}
		defs[d.ItemKey] = merged
		return i.store.UpsertDefinition(ctx, merged)
	}
	defs[d.ItemKey] = d
	return i.store.UpsertDefinition(ctx, d)
}

func (i *Importer) definitionForEntry(entry wiktextractEntry, createdAt float64) (domain.Definition, bool, error) {
	if entry.Word == "" {
		return domain.Definition{}, false, nil
	}
	gloss := glossForEntry(entry)
	if gloss == "" {
		return domain.Definition{}, false, nil
	}
	key, err := i.opts.Language.ResolveKey(entry.Word)
	if err != nil {
		return domain.Definition{}, false, err
	}
	if key == "" {
		return domain.Definition{}, false, nil
	}
	return domain.Definition{
		Language:        i.opts.Language.Code(),
		ItemKey:         key,
		Source:          domain.DefinitionSourceWiktionary,
		Gloss:           gloss,
		GrammaticalNote: noteForEntry(entry),
		Example:         exampleForEntry(entry),
		Etymology:       strings.TrimSpace(entry.EtymologyText),
		CreatedAt:       createdAt,
	}, true, nil
}

type wiktextractEntry struct {
	Word          string             `json:"word"`
	LangCode      string             `json:"lang_code"`
	Pos           string             `json:"pos"`
	Senses        []wiktextractSense `json:"senses"`
	Forms         []wiktextractForm  `json:"forms"`
	EtymologyText string             `json:"etymology_text"`
}

type wiktextractSense struct {
	Glosses    []string             `json:"glosses"`
	RawGlosses []string             `json:"raw_glosses"`
	Tags       []string             `json:"tags"`
	Examples   []wiktextractExample `json:"examples"`
}

type wiktextractExample struct {
	Text string `json:"text"`
}

type wiktextractForm struct {
	Form string   `json:"form"`
	Tags []string `json:"tags"`
}

func glossForEntry(entry wiktextractEntry) string {
	glosses := collectGlosses(entry.Senses, false)
	if len(glosses) == 0 {
		glosses = collectGlosses(entry.Senses, true)
	}
	if len(glosses) > 3 {
		glosses = glosses[:3]
	}
	return strings.Join(glosses, "; ")
}

func collectGlosses(senses []wiktextractSense, includeLowPriority bool) []string {
	var out []string
	seen := make(map[string]bool)
	for _, s := range senses {
		if !includeLowPriority && lowPrioritySense(s.Tags) {
			continue
		}
		gloss := firstNonEmpty(s.Glosses)
		if gloss == "" {
			gloss = firstNonEmpty(s.RawGlosses)
		}
		gloss = strings.TrimSpace(gloss)
		if gloss == "" || seen[gloss] {
			continue
		}
		out = append(out, gloss)
		seen[gloss] = true
	}
	return out
}

func noteForEntry(entry wiktextractEntry) string {
	return strings.TrimSpace(entry.Pos)
}

func exampleForEntry(entry wiktextractEntry) string {
	for _, s := range entry.Senses {
		for _, ex := range s.Examples {
			if text := strings.TrimSpace(ex.Text); text != "" {
				return text
			}
		}
	}
	return ""
}

func usableForm(f wiktextractForm) bool {
	form := strings.TrimSpace(f.Form)
	if form == "" || form == "-" || strings.ContainsAny(form, " \t\r\n") {
		return false
	}
	if !containsLetter(form) {
		return false
	}
	for _, tag := range f.Tags {
		switch strings.ToLower(tag) {
		case "romanization", "transliteration", "table-tags", "no-table-tags", "canonical":
			return false
		}
	}
	return true
}

func containsLetter(s string) bool {
	for _, r := range s {
		if unicode.IsLetter(r) {
			return true
		}
	}
	return false
}

func lowPrioritySense(tags []string) bool {
	for _, tag := range tags {
		switch strings.ToLower(tag) {
		case "obsolete", "archaic", "dated", "rare", "historical", "poetic":
			return true
		}
	}
	return false
}

func mergeDefinitions(a, b domain.Definition) domain.Definition {
	a.Gloss = mergeText(a.Gloss, b.Gloss, "; ", 6)
	a.GrammaticalNote = mergeText(a.GrammaticalNote, b.GrammaticalNote, "; ", 4)
	if a.Example == "" {
		a.Example = b.Example
	}
	if a.Etymology == "" {
		a.Etymology = b.Etymology
	}
	return a
}

func mergeText(a, b, sep string, maxParts int) string {
	a, b = strings.TrimSpace(a), strings.TrimSpace(b)
	if b == "" || a == b {
		return a
	}
	if a == "" {
		return b
	}
	parts := strings.Split(a, sep)
	for _, part := range parts {
		if strings.TrimSpace(part) == b {
			return a
		}
	}
	if len(parts) >= maxParts {
		return a
	}
	return a + sep + b
}

func firstNonEmpty(values []string) string {
	for _, v := range values {
		if s := strings.TrimSpace(v); s != "" {
			return s
		}
	}
	return ""
}
