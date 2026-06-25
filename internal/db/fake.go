package db

import (
	"context"
	"errors"
	"sort"
	"sync"
	"time"

	"github.com/dleiferives/tifl/internal/domain"
	"github.com/dleiferives/tifl/internal/id"
)

// FakeRepository is an in-memory Repository for fast handler/domain tests: no
// database, no migrations, no I/O. It reproduces the observable behaviour the
// SQLite and Postgres backends share — id assignment, ErrNotFound, unique
// constraints, foreign-key rejection, COALESCE-style frequency preservation, and
// per-user scoping — so the same parity suite passes against all three. It is
// not a persistence layer; everything is lost when the process exits.
type FakeRepository struct {
	mu sync.Mutex

	users      map[string]domain.User // user_id -> user
	emailIndex map[string]string      // email -> user_id (unique)
	refresh    map[string]domain.RefreshToken
	languages  map[string]domain.Language
	items      map[string]domain.KnowledgeItem // item_id -> item
	itemKeys   map[string]string               // language\x00type\x00key -> item_id
	knowledge  map[string]domain.UserKnowledge // user_id\x00item_id -> state
	surface    map[string]domain.ReaderSurfaceLevel
	llmCalls   []domain.LLMCall             // append-only audit log
	readerEvts []domain.ReaderEvent         // append-only reader signal log
	readerIDs  map[string]bool              // event_id set, for idempotent insert
	defs       map[string]domain.Definition // language\x00key\x00source -> definition
	defImports map[string]domain.DefinitionImport
	userDefs   map[string]domain.UserDefinition // user_id\x00language\x00key -> user definition
	breakdowns map[string]domain.Breakdown      // scope\x00language\x00cacheKey -> breakdown
	structures map[string]domain.SentenceStructure
	phrases    map[string]domain.CachedPhrase
	skills     map[string]domain.Skill       // skill_id -> skill
	itemSkills map[string]map[string]bool    // item_id -> skill_id set
	userSkills map[string]domain.UserSkillXP // user_id\x00skill_id -> state
	skillLogs  []domain.TaskSkillXPLog       // append-only skill XP log

	// Generation-pipeline state.
	sessions   map[string]domain.Session                    // session_id -> session
	stages     map[string]map[string]domain.GenerationStage // session_id -> stage -> row
	stories    map[string]domain.Story                      // story_id -> story
	tokens     map[string][]domain.StoryToken               // story_id -> ordered tokens
	glossary   map[string][]domain.StoryGlossaryEntry       // story_id -> entries
	tasks      map[string]domain.Task                       // task_id -> task
	targets    map[string][]string                          // task_id -> item_ids
	phraseSets map[string]domain.PhraseSet                  // session_id -> phrase set
}

// compile-time assertion that we satisfy the interface.
var _ Repository = (*FakeRepository)(nil)

// NewFake returns an empty in-memory repository ready to use.
func NewFake() *FakeRepository {
	return &FakeRepository{
		users:      make(map[string]domain.User),
		emailIndex: make(map[string]string),
		refresh:    make(map[string]domain.RefreshToken),
		languages:  make(map[string]domain.Language),
		items:      make(map[string]domain.KnowledgeItem),
		itemKeys:   make(map[string]string),
		knowledge:  make(map[string]domain.UserKnowledge),
		surface:    make(map[string]domain.ReaderSurfaceLevel),
		readerIDs:  make(map[string]bool),
		defs:       make(map[string]domain.Definition),
		defImports: make(map[string]domain.DefinitionImport),
		userDefs:   make(map[string]domain.UserDefinition),
		breakdowns: make(map[string]domain.Breakdown),
		structures: make(map[string]domain.SentenceStructure),
		phrases:    make(map[string]domain.CachedPhrase),
		skills:     make(map[string]domain.Skill),
		itemSkills: make(map[string]map[string]bool),
		userSkills: make(map[string]domain.UserSkillXP),
		sessions:   make(map[string]domain.Session),
		stages:     make(map[string]map[string]domain.GenerationStage),
		stories:    make(map[string]domain.Story),
		tokens:     make(map[string][]domain.StoryToken),
		glossary:   make(map[string][]domain.StoryGlossaryEntry),
		tasks:      make(map[string]domain.Task),
		targets:    make(map[string][]string),
		phraseSets: make(map[string]domain.PhraseSet),
	}
}

func (r *FakeRepository) Migrate(context.Context) error { return nil }
func (r *FakeRepository) Close() error                  { return nil }

// --- users -----------------------------------------------------------------

func (r *FakeRepository) CreateUser(_ context.Context, u domain.User) (domain.User, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, dup := r.emailIndex[u.Email]; dup {
		return domain.User{}, errFakeUnique("users.email")
	}
	if u.UserID == "" {
		u.UserID = id.New()
	}
	if _, dup := r.users[u.UserID]; dup {
		return domain.User{}, errFakeUnique("users.user_id")
	}
	if u.CreatedAt == 0 {
		u.CreatedAt = float64(time.Now().Unix())
	}
	u.Settings = cloneJSONMap(u.Settings)
	u.LastLogin = cloneFloat(u.LastLogin)
	r.users[u.UserID] = u
	r.emailIndex[u.Email] = u.UserID
	return u, nil
}

func (r *FakeRepository) GetUser(_ context.Context, userID string) (domain.User, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	u, ok := r.users[userID]
	if !ok {
		return domain.User{}, ErrNotFound
	}
	return cloneUser(u), nil
}

func (r *FakeRepository) GetUserByEmail(_ context.Context, email string) (domain.User, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	uid, ok := r.emailIndex[email]
	if !ok {
		return domain.User{}, ErrNotFound
	}
	return cloneUser(r.users[uid]), nil
}

func (r *FakeRepository) EnsureLocalUser(ctx context.Context) (domain.User, error) {
	if u, err := r.GetUser(ctx, domain.LocalUserID); err == nil {
		return u, nil
	}
	return r.CreateUser(ctx, domain.User{
		UserID: domain.LocalUserID,
		Email:  "local@tifl.local",
	})
}

func (r *FakeRepository) UpdateUserLastLogin(_ context.Context, userID string, at float64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	u, ok := r.users[userID]
	if !ok {
		return ErrNotFound
	}
	u.LastLogin = &at
	r.users[userID] = u
	return nil
}

func (r *FakeRepository) GetUserProfile(_ context.Context, userID string) (domain.UserProfile, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	user, ok := r.users[userID]
	if !ok {
		return domain.UserProfile{}, ErrNotFound
	}
	return profileFromSettings(user.UserID, user.Settings, r.firstEnabledLanguageLocked()), nil
}

func (r *FakeRepository) UpdateUserProfile(_ context.Context, userID string, patch domain.UserProfilePatch) (domain.UserProfile, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if patch.ActiveLanguage != nil {
		language, ok := r.languages[*patch.ActiveLanguage]
		if !ok || !language.Enabled {
			return domain.UserProfile{}, invalidProfile("active_language %q is not enabled", *patch.ActiveLanguage)
		}
	}
	user, ok := r.users[userID]
	if !ok {
		return domain.UserProfile{}, ErrNotFound
	}
	profile := applyProfilePatch(profileFromSettings(user.UserID, user.Settings, r.firstEnabledLanguageLocked()), patch)
	if err := validateProfile(profile); err != nil {
		return domain.UserProfile{}, err
	}
	user.Settings = settingsWithProfile(user.Settings, profile)
	r.users[userID] = cloneUser(user)
	return profile, nil
}

func (r *FakeRepository) firstEnabledLanguageLocked() string {
	out := make([]domain.Language, 0, len(r.languages))
	for _, l := range r.languages {
		out = append(out, l)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Code < out[j].Code })
	return firstEnabledLanguage(out)
}

func (r *FakeRepository) CreateRefreshToken(_ context.Context, token domain.RefreshToken) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.users[token.UserID]; !ok {
		return errFakeFK("refresh_tokens.user_id")
	}
	if _, exists := r.refresh[token.TokenHash]; exists {
		return errFakeUnique("refresh_tokens.token_hash")
	}
	r.refresh[token.TokenHash] = cloneRefreshToken(token)
	return nil
}

func (r *FakeRepository) GetRefreshToken(_ context.Context, tokenHash string) (domain.RefreshToken, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	token, ok := r.refresh[tokenHash]
	if !ok {
		return domain.RefreshToken{}, ErrNotFound
	}
	return cloneRefreshToken(token), nil
}

func (r *FakeRepository) RotateRefreshToken(_ context.Context, oldHash string, next domain.RefreshToken, now float64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	old, ok := r.refresh[oldHash]
	if !ok || old.ExpiresAt <= now {
		return ErrNotFound
	}
	if old.ReplacedByHash != nil {
		for hash, token := range r.refresh {
			if token.FamilyID == old.FamilyID && token.RevokedAt == nil {
				token.RevokedAt = cloneFloat(&now)
				r.refresh[hash] = token
			}
		}
		return ErrRefreshTokenReuse
	}
	if old.RevokedAt != nil {
		return ErrNotFound
	}
	if next.FamilyID != old.FamilyID || next.UserID != old.UserID {
		return errors.New("db: refresh rotation family/user mismatch")
	}
	if _, exists := r.refresh[next.TokenHash]; exists {
		return errFakeUnique("refresh_tokens.token_hash")
	}
	old.RevokedAt = cloneFloat(&now)
	old.ReplacedByHash = cloneStr(&next.TokenHash)
	r.refresh[oldHash] = old
	r.refresh[next.TokenHash] = cloneRefreshToken(next)
	return nil
}

func (r *FakeRepository) RevokeRefreshToken(_ context.Context, tokenHash string, now float64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	token, ok := r.refresh[tokenHash]
	if !ok {
		return nil
	}
	if token.RevokedAt == nil {
		token.RevokedAt = cloneFloat(&now)
		r.refresh[tokenHash] = token
	}
	return nil
}

func (r *FakeRepository) RevokeAllRefreshTokens(_ context.Context, userID string, now float64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for hash, token := range r.refresh {
		if token.UserID == userID && token.RevokedAt == nil {
			token.RevokedAt = cloneFloat(&now)
			r.refresh[hash] = token
		}
	}
	return nil
}

// --- languages -------------------------------------------------------------

func (r *FakeRepository) UpsertLanguage(_ context.Context, l domain.Language) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.languages[l.Code] = l
	return nil
}

func (r *FakeRepository) GetLanguage(_ context.Context, code string) (domain.Language, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	l, ok := r.languages[code]
	if !ok {
		return domain.Language{}, ErrNotFound
	}
	return l, nil
}

func (r *FakeRepository) ListLanguages(_ context.Context) ([]domain.Language, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]domain.Language, 0, len(r.languages))
	for _, l := range r.languages {
		out = append(out, l)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Code < out[j].Code })
	return out, nil
}

// --- knowledge items -------------------------------------------------------

func (r *FakeRepository) UpsertKnowledgeItem(_ context.Context, item domain.KnowledgeItem) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	uk := item.Language + "\x00" + item.ItemType + "\x00" + item.Key
	if existingID, ok := r.itemKeys[uk]; ok {
		// Update in place; preserve the canonical id and COALESCE frequency
		// (a zero incoming frequency does not clobber a known rank).
		cur := r.items[existingID]
		cur.Metadata = cloneMeta(item.Metadata)
		if item.Frequency > 0 {
			cur.Frequency = item.Frequency
		}
		r.items[existingID] = cur
		return existingID, nil
	}
	if item.ItemID == "" {
		item.ItemID = id.New()
	}
	item.Metadata = cloneMeta(item.Metadata)
	r.items[item.ItemID] = item
	r.itemKeys[uk] = item.ItemID
	return item.ItemID, nil
}

func (r *FakeRepository) GetKnowledgeItem(_ context.Context, itemID string) (domain.KnowledgeItem, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	it, ok := r.items[itemID]
	if !ok {
		return domain.KnowledgeItem{}, ErrNotFound
	}
	return cloneItem(it), nil
}

func (r *FakeRepository) ListKnowledgeItems(_ context.Context, language string) ([]domain.KnowledgeItem, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []domain.KnowledgeItem
	for _, it := range r.items {
		if it.Language == language {
			out = append(out, cloneItem(it))
		}
	}
	// Mirror "ORDER BY frequency IS NULL, frequency, key": ranked items first
	// (ascending rank), then unranked, both broken by key.
	sort.Slice(out, func(i, j int) bool {
		ri, rj := out[i].Frequency > 0, out[j].Frequency > 0
		if ri != rj {
			return ri // ranked (true) sorts before unranked (false)
		}
		if ri && out[i].Frequency != out[j].Frequency {
			return out[i].Frequency < out[j].Frequency
		}
		return out[i].Key < out[j].Key
	})
	return out, nil
}

// --- user knowledge --------------------------------------------------------

func (r *FakeRepository) UpsertUserKnowledge(_ context.Context, uk domain.UserKnowledge) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	// Foreign-key parity: reject unknown users or items, like the real backends.
	if _, ok := r.users[uk.UserID]; !ok {
		return errFakeFK("user_knowledge.user_id")
	}
	if _, ok := r.items[uk.ItemID]; !ok {
		return errFakeFK("user_knowledge.item_id")
	}
	if uk.AcquisitionStage == "" {
		uk.AcquisitionStage = domain.StageUnseen
	}
	uk.LastSeen = cloneFloat(uk.LastSeen)
	uk.LastTargeted = cloneFloat(uk.LastTargeted)
	uk.ConfidenceScore = cloneFloat(uk.ConfidenceScore)
	uk.NextTargetAfter = cloneFloat(uk.NextTargetAfter)
	r.knowledge[uk.UserID+"\x00"+uk.ItemID] = uk
	return nil
}

func (r *FakeRepository) UserKnowledge(_ context.Context, userID, language string) ([]domain.UserKnowledge, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []domain.UserKnowledge
	for _, uk := range r.knowledge {
		if uk.UserID != userID {
			continue
		}
		if it, ok := r.items[uk.ItemID]; !ok || it.Language != language {
			continue
		}
		out = append(out, cloneUK(uk))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ItemID < out[j].ItemID })
	return out, nil
}

func (r *FakeRepository) GetUserKnowledgeItem(_ context.Context, userID, itemID string) (domain.UserKnowledge, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	uk, ok := r.knowledge[userID+"\x00"+itemID]
	if !ok {
		return domain.UserKnowledge{}, ErrNotFound
	}
	return cloneUK(uk), nil
}

func (r *FakeRepository) LoadReaderKnowledge(_ context.Context, userID, language string) ([]domain.ReaderKnowledge, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []domain.ReaderKnowledge
	for _, uk := range r.knowledge {
		if uk.UserID != userID {
			continue
		}
		it, ok := r.items[uk.ItemID]
		if !ok || it.Language != language {
			continue
		}
		out = append(out, domain.ReaderKnowledge{ItemKey: it.Key, Level: uk.Level, LookupCount: uk.LookupCount})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ItemKey < out[j].ItemKey })
	return out, nil
}

func (r *FakeRepository) LoadReaderSurfaceLevels(_ context.Context, userID, language string) ([]domain.ReaderSurfaceLevel, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []domain.ReaderSurfaceLevel
	for _, row := range r.surface {
		if row.UserID == userID && row.Language == language {
			out = append(out, row)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].ItemKey != out[j].ItemKey {
			return out[i].ItemKey < out[j].ItemKey
		}
		return out[i].SurfaceKey < out[j].SurfaceKey
	})
	return out, nil
}

func (r *FakeRepository) UpsertReaderSurfaceLevel(_ context.Context, userID string, row domain.ReaderSurfaceLevel) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.users[userID]; !ok {
		return errFakeFK("reader_surface_levels.user_id")
	}
	if _, ok := r.languages[row.Language]; !ok {
		return errFakeFK("reader_surface_levels.language")
	}
	if row.UpdatedAt == 0 {
		row.UpdatedAt = float64(time.Now().Unix())
	}
	row.UserID = userID
	r.surface[surfaceLevelKey(userID, row.Language, row.ItemKey, row.SurfaceKey)] = row
	return nil
}

// --- definitions & breakdowns ----------------------------------------------

func (r *FakeRepository) ListDefinitions(_ context.Context, language, itemKey string) ([]domain.Definition, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []domain.Definition
	for _, d := range r.defs {
		if d.Language == language && d.ItemKey == itemKey {
			out = append(out, d)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Source < out[j].Source })
	return out, nil
}

func (r *FakeRepository) UpsertDefinition(_ context.Context, d domain.Definition) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if d.CreatedAt == 0 {
		d.CreatedAt = float64(time.Now().Unix())
	}
	r.defs[d.Language+"\x00"+d.ItemKey+"\x00"+d.Source] = d
	return nil
}

func (r *FakeRepository) UpsertDefinitionImport(_ context.Context, imp domain.DefinitionImport) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.defImports[imp.ImportID] = cloneDefinitionImport(imp)
	return nil
}

func (r *FakeRepository) GetDefinitionImport(_ context.Context, importID string) (domain.DefinitionImport, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	imp, ok := r.defImports[importID]
	if !ok {
		return domain.DefinitionImport{}, ErrNotFound
	}
	return cloneDefinitionImport(imp), nil
}

func (r *FakeRepository) GetUserDefinition(_ context.Context, userID, language, itemKey string) (domain.UserDefinition, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	d, ok := r.userDefs[userID+"\x00"+language+"\x00"+itemKey]
	if !ok {
		return domain.UserDefinition{}, ErrNotFound
	}
	return d, nil
}

func (r *FakeRepository) UpsertUserDefinition(_ context.Context, d domain.UserDefinition) (domain.UserDefinition, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	key := d.UserID + "\x00" + d.Language + "\x00" + d.ItemKey
	now := float64(time.Now().Unix())
	if d.CreatedAt == 0 {
		if existing, ok := r.userDefs[key]; ok {
			d.CreatedAt = existing.CreatedAt
		} else {
			d.CreatedAt = now
		}
	}
	if d.UpdatedAt == 0 {
		d.UpdatedAt = now
	}
	r.userDefs[key] = d
	return d, nil
}

func (r *FakeRepository) DeleteUserDefinition(_ context.Context, userID, language, itemKey string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.userDefs, userID+"\x00"+language+"\x00"+itemKey)
	return nil
}

func (r *FakeRepository) GetBreakdown(_ context.Context, scope domain.BreakdownScope, language, cacheKey string) (domain.Breakdown, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	b, ok := r.breakdowns[string(scope)+"\x00"+language+"\x00"+cacheKey]
	if !ok {
		return domain.Breakdown{}, ErrNotFound
	}
	b.Content = cloneMeta(b.Content)
	return b, nil
}

func (r *FakeRepository) UpsertBreakdown(_ context.Context, b domain.Breakdown) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if b.CreatedAt == 0 {
		b.CreatedAt = float64(time.Now().Unix())
	}
	b.Content = cloneMeta(b.Content)
	r.breakdowns[string(b.Scope)+"\x00"+b.Language+"\x00"+b.CacheKey] = b
	return nil
}

func (r *FakeRepository) GetSentenceStructure(_ context.Context, language, structureKey string) (domain.SentenceStructure, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	st, ok := r.structures[language+"\x00"+structureKey]
	if !ok {
		return domain.SentenceStructure{}, ErrNotFound
	}
	return cloneSentenceStructure(st), nil
}

func (r *FakeRepository) UpsertSentenceStructure(_ context.Context, st domain.SentenceStructure) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if st.CreatedAt == 0 {
		st.CreatedAt = float64(time.Now().Unix())
	}
	if st.UpdatedAt == 0 {
		st.UpdatedAt = st.CreatedAt
	}
	r.structures[st.Language+"\x00"+st.StructureKey] = cloneSentenceStructure(st)
	return nil
}

func (r *FakeRepository) FindPhrases(_ context.Context, language string, normalizedTexts []string) ([]domain.CachedPhrase, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	wanted := make(map[string]bool, len(normalizedTexts))
	for _, text := range normalizedTexts {
		wanted[text] = true
	}
	var out []domain.CachedPhrase
	for _, p := range r.phrases {
		if p.Language == language && wanted[p.NormalizedText] {
			out = append(out, cloneCachedPhrase(p))
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].NormalizedText == out[j].NormalizedText {
			return out[i].PhraseKey < out[j].PhraseKey
		}
		return out[i].NormalizedText < out[j].NormalizedText
	})
	return out, nil
}

func (r *FakeRepository) UpsertPhrase(_ context.Context, p domain.CachedPhrase) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if p.CreatedAt == 0 {
		p.CreatedAt = float64(time.Now().Unix())
	}
	if p.UpdatedAt == 0 {
		p.UpdatedAt = p.CreatedAt
	}
	r.phrases[p.Language+"\x00"+p.PhraseKey] = cloneCachedPhrase(p)
	return nil
}

// --- llm calls -------------------------------------------------------------

func (r *FakeRepository) InsertLLMCall(_ context.Context, c domain.LLMCall) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if c.CallID == "" {
		c.CallID = id.New()
	}
	if c.CalledAt == 0 {
		c.CalledAt = float64(time.Now().Unix())
	}
	r.llmCalls = append(r.llmCalls, cloneLLMCall(c))
	return nil
}

// --- reader events ---------------------------------------------------------

func (r *FakeRepository) InsertReaderEvents(_ context.Context, events []domain.ReaderEvent) ([]domain.ReaderEvent, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var inserted []domain.ReaderEvent
	for _, e := range events {
		if e.EventID == "" {
			e.EventID = id.New()
		}
		if r.readerIDs[e.EventID] {
			continue // idempotent on event_id, like the SQL backends
		}
		if e.OccurredAt == 0 {
			e.OccurredAt = float64(time.Now().Unix())
		}
		r.readerIDs[e.EventID] = true
		r.readerEvts = append(r.readerEvts, cloneReaderEvent(e))
		inserted = append(inserted, cloneReaderEvent(e))
	}
	return inserted, nil
}

func (r *FakeRepository) HasReaderEvents(_ context.Context, userID, storyID string) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, e := range r.readerEvts {
		if e.UserID == userID && e.StoryID == storyID {
			return true, nil
		}
	}
	return false, nil
}

// ReaderEvents returns a copy of the recorded reader events, for test inspection.
// Not part of the Repository interface — only the in-memory backend exposes it.
func (r *FakeRepository) ReaderEvents() []domain.ReaderEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]domain.ReaderEvent, len(r.readerEvts))
	for i, e := range r.readerEvts {
		out[i] = cloneReaderEvent(e)
	}
	return out
}

func surfaceLevelKey(userID, language, itemKey, surfaceKey string) string {
	return userID + "\x00" + language + "\x00" + itemKey + "\x00" + surfaceKey
}

// LLMCalls returns a copy of the recorded calls, for test inspection. It is not
// part of the Repository interface — only the in-memory backend exposes it.
func (r *FakeRepository) LLMCalls() []domain.LLMCall {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]domain.LLMCall, len(r.llmCalls))
	for i, c := range r.llmCalls {
		out[i] = cloneLLMCall(c)
	}
	return out
}

// --- clone / error helpers -------------------------------------------------

func cloneMeta(m map[string]any) map[string]any {
	return cloneJSONMap(m)
}

func cloneFloat(p *float64) *float64 {
	if p == nil {
		return nil
	}
	v := *p
	return &v
}

func cloneUser(u domain.User) domain.User {
	u.Settings = cloneJSONMap(u.Settings)
	u.LastLogin = cloneFloat(u.LastLogin)
	return u
}

func cloneRefreshToken(token domain.RefreshToken) domain.RefreshToken {
	token.RevokedAt = cloneFloat(token.RevokedAt)
	token.ReplacedByHash = cloneStr(token.ReplacedByHash)
	return token
}

func cloneItem(it domain.KnowledgeItem) domain.KnowledgeItem {
	it.Metadata = cloneJSONMap(it.Metadata)
	return it
}

func cloneInt(p *int) *int {
	if p == nil {
		return nil
	}
	v := *p
	return &v
}

func cloneStr(p *string) *string {
	if p == nil {
		return nil
	}
	v := *p
	return &v
}

func cloneReaderEvent(e domain.ReaderEvent) domain.ReaderEvent {
	e.SessionID = cloneStr(e.SessionID)
	e.Position = cloneInt(e.Position)
	e.Value = cloneStr(e.Value)
	return e
}

func cloneLLMCall(c domain.LLMCall) domain.LLMCall {
	c.SessionID = cloneStr(c.SessionID)
	c.UserID = cloneStr(c.UserID)
	c.InputTokens = cloneInt(c.InputTokens)
	c.OutputTokens = cloneInt(c.OutputTokens)
	c.LatencyMs = cloneInt(c.LatencyMs)
	c.ErrorDetail = cloneStr(c.ErrorDetail)
	return c
}

func cloneDefinitionImport(imp domain.DefinitionImport) domain.DefinitionImport {
	imp.CompletedAt = cloneFloat(imp.CompletedAt)
	return imp
}

func cloneSentenceStructure(st domain.SentenceStructure) domain.SentenceStructure {
	st.Graph = cloneSyntaxGraph(st.Graph)
	st.PhraseKeys = append([]string(nil), st.PhraseKeys...)
	return st
}

func cloneCachedPhrase(p domain.CachedPhrase) domain.CachedPhrase {
	p.Graph = cloneSyntaxGraph(p.Graph)
	p.Metadata = cloneMeta(p.Metadata)
	return p
}

func cloneSyntaxGraph(g domain.SyntaxGraph) domain.SyntaxGraph {
	g.Roots = append([]string(nil), g.Roots...)
	g.Nodes = append([]domain.SyntaxNode(nil), g.Nodes...)
	for i := range g.Nodes {
		g.Nodes[i].Features = cloneMeta(g.Nodes[i].Features)
	}
	g.Edges = append([]domain.SyntaxEdge(nil), g.Edges...)
	for i := range g.Edges {
		g.Edges[i].Features = cloneMeta(g.Edges[i].Features)
	}
	return g
}

func cloneUK(uk domain.UserKnowledge) domain.UserKnowledge {
	uk.LastSeen = cloneFloat(uk.LastSeen)
	uk.LastTargeted = cloneFloat(uk.LastTargeted)
	uk.ConfidenceScore = cloneFloat(uk.ConfidenceScore)
	uk.NextTargetAfter = cloneFloat(uk.NextTargetAfter)
	return uk
}

type fakeErr struct{ msg string }

func (e fakeErr) Error() string { return e.msg }

func errFakeUnique(what string) error { return fakeErr{"fake: unique violation on " + what} }
func errFakeFK(what string) error     { return fakeErr{"fake: foreign-key violation on " + what} }
