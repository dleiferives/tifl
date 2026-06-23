package domain

// LocalUserID is the synthetic user injected when AuthMode is "none" (desktop
// local). The repository code is identical to the multi-tenant cloud case; only
// the user_id differs. See context/backend-server.md ("Multi-tenancy").
const LocalUserID = "local"

// User is a registered identity. See context/database-schema.md ("users").
type User struct {
	UserID       string
	Email        string
	PasswordHash string         // argon2id; empty for the local user
	CreatedAt    float64        // Unix seconds
	LastLogin    *float64       // Unix seconds; nil if never
	Settings     map[string]any // theme / UI prefs; nil when unset
}

// RefreshToken is one server-side refresh credential. The raw token is only
// returned to the client cookie; storage keeps its SHA-256 digest. FamilyID
// groups one login/device's rotated tokens so replay revokes that session
// without terminating the user's other concurrent sessions.
type RefreshToken struct {
	TokenHash      string
	FamilyID       string
	UserID         string
	IssuedAt       float64
	ExpiresAt      float64
	RevokedAt      *float64
	ReplacedByHash *string
}

// Language is a catalogue row for a registered language plugin. Populated at
// startup from whatever plugins are compiled in. See
// context/database-schema.md ("languages").
type Language struct {
	Code        string // "grc", "el", "ar", "zh"
	Name        string
	KeyStrategy string // surface | lemma | root | stem
	Enabled     bool   // false = registered but not exposed to users
}

// LLMCall is one record of an outbound model call, written by the gateway client
// (internal/llm) after every call — the client, not the gateway, has the
// session/user context. It backs cost tracking and the AI logs the UI inspects.
// Nullable columns are pointers so "not applicable" is distinct from zero. See
// context/prompting-system.md ("The Outbound Channel") and
// context/database-schema.md ("llm_calls").
type LLMCall struct {
	CallID        string
	SessionID     *string // nil for non-session calls (e.g. scope check)
	UserID        *string // nil when the call has no user (system tasks)
	Kind          string  // story_generator | task_* | grader | assessor | scope_check
	PromptVersion string  // the builder's Version(), for regression correlation
	Model         string  // model actually used (from the gateway response)
	InputTokens   *int    // prompt tokens; nil if the provider omitted usage
	OutputTokens  *int    // completion tokens; nil if omitted
	LatencyMs     *int    // round-trip latency including retries; nil if unmeasured
	Status        string  // success | error | timeout
	ErrorDetail   *string // populated when Status != success
	CalledAt      float64 // Unix seconds
}

// UserKnowledge is a user's acquisition state for one knowledge item — the
// central table the selection layer reads on every generation request. The
// nullable fields are pointers so "never set" is distinct from zero. See
// context/knowledge-acquisition.md ("The user_knowledge Table").
type UserKnowledge struct {
	UserID           string
	ItemID           string
	AcquisitionStage AcquisitionStage
	Level            ReaderLevel // learner's self-rating (reader); "" = unseen
	ExposureCount    int
	ContextVariety   int // distinct stories it appeared in
	LookupCount      int // Space presses in the reader (strong "not acquired")
	TaskCorrect      int
	TaskTotal        int
	LastSeen         *float64 // Unix seconds
	LastTargeted     *float64 // last time the selector put it in targets[]
	ConfidenceScore  *float64 // 0..1, computed by the predictor
	NextTargetAfter  *float64 // internal SRS-like scheduling; not user-visible
}

// Skill is a language-defined competency shown in the read-only skill tree.
// Skills are seeded from language/plugin definitions and grouped by category for
// display; user progress lives separately in user_skill_xp.
type Skill struct {
	SkillID     string
	Language    string
	Name        string
	Description string
	Category    string
	TierCount   int
	XPPerTier   int
	SortOrder   int
}

// UserSkillXP is the user's current XP/tier row for a skill. Missing rows mean
// tier 0 with 0 XP, so the skill tree can show untouched skills.
type UserSkillXP struct {
	UserID         string
	SkillID        string
	XP             int
	Tier           int
	PendingVerify  bool
	LastVerifiedAt *float64
	UpdatedAt      float64
}

// SkillProgress combines a skill definition with the current user's progress.
type SkillProgress struct {
	Skill
	XP             int
	Tier           int
	PendingVerify  bool
	LastVerifiedAt *float64
	UpdatedAt      *float64
}
