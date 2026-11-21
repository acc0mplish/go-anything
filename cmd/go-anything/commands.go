package main

import (
	"fmt"
	"io"
)

// toolEntry is one capability, described once. The command line and the MCP server both read this
// table, so a capability cannot be exposed in one and silently missing from the other.
type toolEntry struct {
	CLI  string // the command, as typed after go-anything
	MCP  string // the MCP tool name; empty when the capability is not exposed to agents
	Note string // why it is or is not exposed
}

var toolRegistry = []toolEntry{
	{"analyze", "analyze", ""},
	{"app record", "app_record", ""},
	{"app replay", "app_replay", ""},
	{"bench", "bench", ""},
	{"commands", "", "lists this table"},
	{"rbac", "", "sends requests with tokens from the environment; run by a person, not an agent"},
	{"secure", "", "runs go vet and govulncheck on the module; run by a person, opt-in on gate via -security"},
	{"coverage", "", "reads a coverage profile; cheap to add when needed"},
	{"doctor", "doctor", ""},
	{"frontdoor", "", "starts a server; not exposed to agents"},
	{"frontdoor scaffold", "", "writes a Go module and starts the original to check it; not exposed to agents"},
	{"fuzz", "fuzz", ""},
	{"from-tests", "", "runs the test command the caller names"},
	{"gate", "gate", ""},
	{"lift", "lift", "the MCP form skips unit commands; the command line runs them"},
	{"map", "map", ""},
	{"mcp", "", "is the MCP server itself"},
	{"profile", "", "builds and runs a benchmark; not exposed to agents"},
	{"record", "record", ""},
	{"setup", "", "writes agent configuration files"},
	{"package", "", "writes release files and cross-compiles with -check; not exposed to agents"},
	{"shadow", "", "runs a program alongside another; not exposed to agents"},
	{"shadow gen", "", "writes a Go package and builds it; not exposed to agents"},
	{"verify", "verify", ""},
	{"wire gen", "", "writes code into the module; not exposed to agents"},
	{"wire refine", "", "runs an agent's shell command; not exposed to agents"},
	{"wire set", "", "changes routing for live traffic; not exposed to agents"},
	{"wire status", "wire_status", ""},
	{"wire verify", "", "builds and starts the program; not exposed to agents"},
}

// cmdCommands prints the table: every command, whether an agent can reach it, and why.
func cmdCommands(stdout io.Writer) error {
	fmt.Fprintf(stdout, "%-14s %-14s %s\n", "COMMAND", "MCP TOOL", "NOTE")
	for _, e := range toolRegistry {
		mcp := e.MCP
		if mcp == "" {
			mcp = "-"
		}
		fmt.Fprintf(stdout, "%-14s %-14s %s\n", e.CLI, mcp, e.Note)
	}
	return nil
}

// mcpToolNames lists the MCP tools the registry declares.
func mcpToolNames() []string {
	var out []string
	for _, e := range toolRegistry {
		if e.MCP != "" {
			out = append(out, e.MCP)
		}
	}
	return out
}
