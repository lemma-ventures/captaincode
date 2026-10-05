package captaincode

// TokenUsage keeps disjoint buckets. Nil fields mean the transport did not
// report that bucket; old totals cannot be split retrospectively.
type TokenUsage struct {
	Input      *int   `json:"input,omitempty"` // fresh input, excludes cache reads and writes
	Output     *int   `json:"output,omitempty"`
	CacheRead  *int   `json:"cache_read,omitempty"`
	CacheWrite *int   `json:"cache_write,omitempty"`
	Source     string `json:"source"`
	Scope      string `json:"scope"` // run or final_message
}

func tokenCount(n int) *int {
	if n < 0 {
		return nil
	}
	return &n
}

func ClaudeTokenUsage(input, output, read, write int) *TokenUsage {
	return &TokenUsage{Input: tokenCount(input), Output: tokenCount(output), CacheRead: tokenCount(read), CacheWrite: tokenCount(write), Source: "claude", Scope: "run"}
}

// OpenAI-compatible input includes cached input. Missing cache detail leaves
// fresh input unknown: treating the whole prompt as fresh would inflate it.
func InclusiveTokenUsage(input, output int, cached *int, source string) *TokenUsage {
	return OptionalInclusiveTokenUsage(tokenCount(input), tokenCount(output), cached, source)
}

func OptionalInclusiveTokenUsage(input, output, cached *int, source string) *TokenUsage {
	u := &TokenUsage{Source: source, Scope: "run"}
	if output != nil {
		u.Output = tokenCount(*output)
	}
	if input != nil && *input >= 0 && cached != nil && *cached >= 0 && *cached <= *input {
		u.CacheRead = tokenCount(*cached)
		u.Input = tokenCount(*input - *cached)
	}
	return u
}

// MergeTokenUsage drops incomplete buckets, rather than calling a partial
// sum a measured total. Used when a repair is folded into the worker result.
func MergeTokenUsage(a, b *TokenUsage) *TokenUsage {
	if a == nil || b == nil || a.Source != b.Source || a.Scope != b.Scope {
		return nil
	}
	add := func(x, y *int) *int {
		if x == nil || y == nil {
			return nil
		}
		return tokenCount(*x + *y)
	}
	return &TokenUsage{add(a.Input, b.Input), add(a.Output, b.Output), add(a.CacheRead, b.CacheRead), add(a.CacheWrite, b.CacheWrite), a.Source, a.Scope}
}

// ClaudeUsageReport preserves presence. A missing cache field is not zero.
type ClaudeUsageReport struct {
	Input      *int `json:"input_tokens"`
	Output     *int `json:"output_tokens"`
	CacheRead  *int `json:"cache_read_input_tokens"`
	CacheWrite *int `json:"cache_creation_input_tokens"`
}

func (r *ClaudeUsageReport) Detail() *TokenUsage {
	if r == nil {
		return nil
	}
	valid := func(n *int) *int {
		if n == nil {
			return nil
		}
		return tokenCount(*n)
	}
	return &TokenUsage{Input: valid(r.Input), Output: valid(r.Output), CacheRead: valid(r.CacheRead), CacheWrite: valid(r.CacheWrite), Source: "claude", Scope: "run"}
}
func (r *ClaudeUsageReport) Total() int {
	if r == nil {
		return 0
	}
	total := 0
	for _, n := range []*int{r.Input, r.Output, r.CacheRead, r.CacheWrite} {
		if n != nil && *n > 0 {
			total += *n
		}
	}
	return total
}
