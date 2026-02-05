package session

import "strconv"

// ClaudeCodeParser parses OTEL events from Claude Code.
// Claude Code emits events like api_request with token usage and cost.
type ClaudeCodeParser struct{}

// NewClaudeCodeParser returns a parser for Claude Code OTEL events.
func NewClaudeCodeParser() *ClaudeCodeParser {
	return &ClaudeCodeParser{}
}

// ParseLogRecord extracts metrics from a Claude Code log record.
// Returns nil if the record doesn't contain relevant metrics.
func (p *ClaudeCodeParser) ParseLogRecord(record OtelLogRecord) *OtelMetricsDelta {
	eventName := getAttr(record.Attributes, "event.name")
	if eventName == "" {
		return nil
	}

	delta := &OtelMetricsDelta{}

	switch eventName {
	case "api_request":
		delta.IsAPIRequest = true
		// Claude Code includes token counts and cost in api_request events
		delta.InputTokens = getIntAttr(record.Attributes, "input_tokens")
		delta.OutputTokens = getIntAttr(record.Attributes, "output_tokens")
		delta.TotalTokens = delta.InputTokens + delta.OutputTokens
		delta.CostUSD = getFloatAttr(record.Attributes, "cost_usd")

	case "tool_result":
		delta.IsToolResult = true
		// tool_result may have its own token counts
		delta.InputTokens = getIntAttr(record.Attributes, "input_tokens")
		delta.OutputTokens = getIntAttr(record.Attributes, "output_tokens")
		delta.TotalTokens = delta.InputTokens + delta.OutputTokens

	default:
		// Other events (user_prompt, tool_decision, etc.) — just mark as activity
		return nil
	}

	// Only return delta if there's actual data
	if delta.InputTokens == 0 && delta.OutputTokens == 0 && delta.CostUSD == 0 && !delta.IsAPIRequest && !delta.IsToolResult {
		return nil
	}

	return delta
}

// getIntAttr extracts an integer attribute value.
func getIntAttr(attrs []OtelAttribute, key string) int64 {
	for _, a := range attrs {
		if a.Key == key {
			// OTEL can send ints as intValue or stringValue
			if a.Value.IntValue != "" {
				if v, err := strconv.ParseInt(a.Value.IntValue, 10, 64); err == nil {
					return v
				}
			}
			if a.Value.StringValue != "" {
				if v, err := strconv.ParseInt(a.Value.StringValue, 10, 64); err == nil {
					return v
				}
			}
		}
	}
	return 0
}

// getFloatAttr extracts a float attribute value.
func getFloatAttr(attrs []OtelAttribute, key string) float64 {
	for _, a := range attrs {
		if a.Key == key {
			if a.Value.StringValue != "" {
				if v, err := strconv.ParseFloat(a.Value.StringValue, 64); err == nil {
					return v
				}
			}
		}
	}
	return 0
}
