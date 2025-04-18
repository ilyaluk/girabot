package tokenserver

type Stats struct {
	TotalTokens       int64 `json:"total_tokens"`
	ExpiredUnassigned int64 `json:"expired_unassigned"`

	ValidTokens            int64 `json:"valid_tokens"`
	ValidTokensAfter10Mins int64 `json:"valid_tokens_after_10_mins"`

	AvailableTokens int64 `json:"available_tokens"`
	AssignedTokens  int64 `json:"assigned_tokens"`
}
