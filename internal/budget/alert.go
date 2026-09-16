package budget

import "time"

// Alert levels: a provider's window is close to its cap, or has reached it
// and the gate is deferring its tasks.
const (
	AlertNear   = "near"
	AlertCapped = "capped"
)

// Alert is one budget message for the notification channels. It lives here,
// with the numbers it describes, so the engine can build one and
// internal/notify can word it without either importing the other.
type Alert struct {
	Provider string
	Level    string
	// TokensUsed is the weighted window usage the gate compares with Cap.
	TokensUsed int
	Cap        int
	// PctOfCap is TokensUsed as a percentage of Cap, not of token_budget:
	// 100 is the point where the gate starts deferring.
	PctOfCap    float64
	WindowHours float64
	// ResetAt is when the window is estimated to drop below the cap. Zero
	// unless the provider is capped.
	ResetAt time.Time
	// Waiting is how many ready tasks are deferred on this provider.
	Waiting int
	// Estimated is whether part of the usage is orch's guess (a CLI that
	// reports no tokens) rather than what the CLI reported.
	Estimated bool
}
