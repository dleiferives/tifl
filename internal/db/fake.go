package db

import (
	"context"
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
	languages  map[string]domain.Language
	items      map[string]domain.KnowledgeItem // item_id -> item
	itemKeys   map[string]string               // language\x00type\x00key -> item_id
	knowledge  map[string]domain.UserKnowledge // user_id\x00item_id -> state
	llmCalls   []domain.LLMCall                // append-only audit log

	// Generation-pipeline state.
	sessions map[string]domain.Session                    // session_id -> session
	stages   map[string]map[string]domain.GenerationStage // session_id -> stage -> row
	stories  map[string]domain.Story                      // story_id -> story
	tokens   map[string][]domain.StoryToken               // story_id -> ordered tokens
	glossary map[string][]domain.StoryGlossaryEntry       // story_id -> entries
	tasks    map[string]domain.Task                       // task_id -> task
	targets  map[string][]string                          // task_id -> item_ids
}

// compile-time assertion that we satisfy the interface.
var _ Repository = (*FakeRepository)(nil)

// NewFake returns an empty in-memory repository ready to use.
func NewFake() *FakeRepository {
	return &FakeRepository{
		users:      make(map[string]domain.User),
		emailIndex: make(map[string]string),
		languages:  make(map[string]domain.Language),
		items:      make(map[string]domain.KnowledgeItem),
		itemKeys:   make(map[string]string),
		knowledge:  make(map[string]domain.UserKnowledge),
		sessions:   make(map[string]domain.Session),
		stages:     make(map[string]map[string]domain.GenerationStage),
		stories:    make(map[string]domain.Story),
		tokens:     make(map[string][]domain.StoryToken),
		glossary:   make(map[string][]domain.StoryGlossaryEntry),
		tasks:      make(map[string]domain.Task),
		targets:    make(map[string][]string),
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
	u.Settings = cloneMeta(u.Settings)
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
	if m == nil {
		return nil
	}
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func cloneFloat(p *float64) *float64 {
	if p == nil {
		return nil
	}
	v := *p
	return &v
}

func cloneUser(u domain.User) domain.User {
	u.Settings = cloneMeta(u.Settings)
	u.LastLogin = cloneFloat(u.LastLogin)
	return u
}

func cloneItem(it domain.KnowledgeItem) domain.KnowledgeItem {
	it.Metadata = cloneMeta(it.Metadata)
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

func cloneLLMCall(c domain.LLMCall) domain.LLMCall {
	c.SessionID = cloneStr(c.SessionID)
	c.UserID = cloneStr(c.UserID)
	c.InputTokens = cloneInt(c.InputTokens)
	c.OutputTokens = cloneInt(c.OutputTokens)
	c.LatencyMs = cloneInt(c.LatencyMs)
	c.ErrorDetail = cloneStr(c.ErrorDetail)
	return c
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
