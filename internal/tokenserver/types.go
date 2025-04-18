package tokenserver

type Stats struct {
	TotalTokens       int64 `json:"total_tokens"`
	ExpiredUnassigned int64 `json:"expired_unassigned"`

	ValidTokens int64 `json:"valid_tokens"`

	AvailableTokens            int64 `json:"available_tokens"`
	AvailableTokensAfter10Mins int64 `json:"available_tokens_after_10_mins"`

	AssignedTokens int64 `json:"assigned_tokens"`
}
