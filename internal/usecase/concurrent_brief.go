package usecase

import (
	"fmt"
	"strings"
)

// ConcurrentBrief is the part of the agent's brief that names the cases where the rewrite answered
// identical concurrent requests differently. It names the cases and the fact, nothing else: a response
// body from one of the racing requests carries no signal an agent can act on and may hold a value a
// report must not carry. English only — this text reaches a coding agent.
func ConcurrentBrief(caseNames []string) string {
	if len(caseNames) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\nThe concurrent-request check sent each recorded case below several times at once, and the rewrite gave different answers to the same concurrent requests (a race or shared state the sequential cases did not reach). Fix every one:\n")
	for _, name := range caseNames {
		// Case names come from recorded traffic too, so the same control-byte escaping applies before
		// the name reaches the agent's terminal.
		fmt.Fprintf(&b, "- %s: identical concurrent requests, different answers\n", escapeCtrl(name))
	}
	return b.String()
}
