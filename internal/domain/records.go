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

// Language is a catalogue row for a registered language plugin. Populated at
// startup from whatever plugins are compiled in. See
// context/database-schema.md ("languages").
type Language struct {
	Code        string // "grc", "el", "ar", "zh"
	Name        string
	KeyStrategy string // surface | lemma | root | stem
	Enabled     bool   // false = registered but not exposed to users
}

// UserKnowledge is a user's acquisition state for one knowledge item — the
// central table the selection layer reads on every generation request. The
// nullable fields are pointers so "never set" is distinct from zero. See
// context/knowledge-acquisition.md ("The user_knowledge Table").
type UserKnowledge struct {
	UserID           string
	ItemID           string
	AcquisitionStage AcquisitionStage
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
