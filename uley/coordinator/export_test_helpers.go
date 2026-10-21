package coordinator

import "github.com/sqo4/uley/internal/protocol"

// TaskPayloadForTest builds a greedy one-question payload for tests in
// other packages.
func TaskPayloadForTest(question string) taskPayload {
	return taskPayload{Messages: []protocol.Message{{Role: "user", Content: question}}, MaxTokens: 20}
}
