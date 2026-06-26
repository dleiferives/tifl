package domain

// AuthSecurityEventType describes what happened during an auth flow without
// coupling storage to a specific HTTP handler decision.
type AuthSecurityEventType string

const (
	AuthSecurityEventFailedAttempt    AuthSecurityEventType = "failed_attempt"
	AuthSecurityEventThrottledAttempt AuthSecurityEventType = "throttled_attempt"
)

// AuthFlow identifies the credential flow that produced an auth security event.
type AuthFlow string

const (
	AuthFlowLogin    AuthFlow = "login"
	AuthFlowRegister AuthFlow = "register"
)

// AuthSecurityEvent is an append-only security log row for login/register
// attempts. EmailHash stores a normalized-email digest, not plaintext
// credentials. SourceAddressBucket stores coarse source-address metadata such as
// an IP prefix or keyed bucket rather than the raw address.
type AuthSecurityEvent struct {
	EventID             string
	EventType           AuthSecurityEventType
	Flow                AuthFlow
	EmailHash           string
	SourceAddressBucket string
	UserID              *string
	CreatedAt           float64
	Details             map[string]any
}

// ListAuthSecurityEventsOptions filters auth security events. Zero values mean
// no filter; Limit <= 0 returns all matching rows.
type ListAuthSecurityEventsOptions struct {
	Limit               int
	Offset              int
	UserID              string
	EventType           AuthSecurityEventType
	Flow                AuthFlow
	EmailHash           string
	SourceAddressBucket string
	CreatedAfter        *float64
	CreatedBefore       *float64
}
