package usecase

import (
	"fmt"

	"github.com/go-anything/go-anything/internal/domain"
)

// Environment is everything the doctor needs to know about the machine. An adapter fills it in;
// the checks below only judge it, so they can be tested without touching the real system.
type Environment struct {
	GoVersion     string // output of `go version`, e.g. "go version go1.27.4 linux/amd64"
	RequiredGo    string // the go.mod requirement, e.g. "1.27.4"
	CovdataWorks  bool   // `go tool covdata` ran
	PprofWorks    bool   // `go tool pprof` ran
	AgentConfigs  []AgentConfig
	ReportDirOK   bool            // the report folder can be created and written
	PortAvailable bool            // a loopback port for the webhook receiver can be bound
	SkillIntact   map[string]bool // per agent: skill installed and equal to the embedded copy
}

// AgentConfig says how one agent's settings look.
type AgentConfig struct {
	Agent      string
	Installed  bool // the agent's own folder exists
	FileExists bool // its settings file exists
	Registered bool // our server entry is present in it
}

// Doctor runs every check and returns the report. It never changes anything.
func Doctor(env Environment) domain.DoctorReport {
	var r domain.DoctorReport
	r.Checks = append(r.Checks, checkGo(env))
	r.Checks = append(r.Checks, checkTools(env)...)
	r.Checks = append(r.Checks, checkAgents(env)...)
	r.Checks = append(r.Checks, checkReportDir(env))
	r.Checks = append(r.Checks, checkPort(env))
	return r
}

func checkGo(env Environment) domain.Check {
	c := domain.Check{Name: "go toolchain"}
	if env.GoVersion == "" {
		c.Level = domain.LevelFail
		c.Detail = "go is not on PATH"
		c.Fix = "install Go " + env.RequiredGo + " or newer and make sure `go` is on PATH"
		return c
	}
	if env.RequiredGo == "" {
		// No go.mod to read a requirement from: we can only say what is installed.
		c.Level = domain.LevelWarn
		c.Detail = env.GoVersion + " (no go.mod found, so the required version is unknown)"
		c.Fix = "run doctor from the module root, or pass -project <module root>"
		return c
	}
	if !domain.GoVersionAtLeast(env.GoVersion, env.RequiredGo) {
		c.Level = domain.LevelFail
		c.Detail = fmt.Sprintf("found %q, module needs %s or newer", env.GoVersion, env.RequiredGo)
		c.Fix = "upgrade Go to " + env.RequiredGo + " or newer"
		return c
	}
	c.Level = domain.LevelOK
	c.Detail = env.GoVersion
	return c
}

// checkTools: coverage and profiling depend on these official `go tool` commands. Without them
// the features still exist but cannot run, so the gap is a warning, not a failure.
func checkTools(env Environment) []domain.Check {
	var out []domain.Check
	out = append(out, toolCheck("go tool covdata (coverage)", env.CovdataWorks, "record -build and coverage will not work"))
	out = append(out, toolCheck("go tool pprof (profile)", env.PprofWorks, "the profile command will not work"))
	return out
}

func toolCheck(name string, works bool, impact string) domain.Check {
	if works {
		return domain.Check{Name: name, Level: domain.LevelOK, Detail: "runs"}
	}
	return domain.Check{Name: name, Level: domain.LevelWarn, Detail: impact,
		Fix: "run `go tool covdata` and `go tool pprof` by hand to see the error"}
}

// checkAgents: an agent that is installed but not connected is a warning (it simply will not use
// go-anything yet). A connected entry with a stale skill is also a warning.
func checkAgents(env Environment) []domain.Check {
	var out []domain.Check
	for _, a := range env.AgentConfigs {
		if !a.Installed {
			continue // not installed: nothing to say
		}
		name := "agent " + a.Agent
		switch {
		case a.Registered && env.SkillIntact[a.Agent]:
			out = append(out, domain.Check{Name: name, Level: domain.LevelOK, Detail: "registered, skill up to date"})
		case a.Registered:
			out = append(out, domain.Check{Name: name, Level: domain.LevelWarn,
				Detail: "registered, but the skill is missing or out of date",
				Fix:    "run `go-anything setup -apply -agent " + a.Agent + "` to refresh the skill"})
		default:
			out = append(out, domain.Check{Name: name, Level: domain.LevelWarn,
				Detail: "installed but not connected to go-anything",
				Fix:    "run `go-anything setup -apply -agent " + a.Agent + "`"})
		}
	}
	return out
}

func checkReportDir(env Environment) domain.Check {
	c := domain.Check{Name: "report folder"}
	if env.ReportDirOK {
		c.Level, c.Detail = domain.LevelOK, "writable"
		return c
	}
	c.Level = domain.LevelFail
	c.Detail = "cannot create or write the report folder"
	c.Fix = "check permissions on the project folder; reports go under .go-anything/reports"
	return c
}

func checkPort(env Environment) domain.Check {
	c := domain.Check{Name: "webhook receiver port"}
	if env.PortAvailable {
		c.Level, c.Detail = domain.LevelOK, "a loopback port can be bound"
		return c
	}
	c.Level = domain.LevelWarn
	c.Detail = "no loopback port could be bound; webhook capture will fail"
	c.Fix = "free a port or pass -webhooks 127.0.0.1:<port> with a port that is free"
	return c
}
