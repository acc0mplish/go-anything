package usecase

import (
	"strings"
	"testing"

	"github.com/go-anything/go-anything/internal/domain"
)

func healthyEnv() Environment {
	return Environment{
		GoVersion:     "go version go1.27.4 linux/amd64",
		RequiredGo:    "1.27.4",
		CovdataWorks:  true,
		PprofWorks:    true,
		ReportDirOK:   true,
		PortAvailable: true,
		SkillIntact:   map[string]bool{"claude": true},
		AgentConfigs: []AgentConfig{
			{Agent: "claude", Installed: true, FileExists: true, Registered: true},
		},
	}
}

func find(r domain.DoctorReport, name string) domain.Check {
	for _, c := range r.Checks {
		if strings.HasPrefix(c.Name, name) {
			return c
		}
	}
	return domain.Check{}
}

func TestDoctorAllHealthy(t *testing.T) {
	r := Doctor(healthyEnv())
	if !r.Healthy() {
		t.Fatalf("a fully set-up machine must be healthy: %+v", r.Checks)
	}
	if n := r.Counts()[domain.LevelWarn]; n != 0 {
		t.Fatalf("a fully set-up machine must have no warnings, got %d: %+v", n, r.Checks)
	}
}

func TestDoctorOldGoIsFailureWithFix(t *testing.T) {
	env := healthyEnv()
	env.GoVersion = "go version go1.26.0 linux/amd64"
	r := Doctor(env)
	c := find(r, "go toolchain")
	if c.Level != domain.LevelFail {
		t.Fatalf("old Go must be a failure, got %s", c.Level)
	}
	if !strings.Contains(c.Fix, "1.27.4") {
		t.Fatalf("the fix must name the required version: %q", c.Fix)
	}
	if r.Healthy() {
		t.Fatal("old Go must make the setup unhealthy")
	}
}

func TestDoctorMissingGoIsFailure(t *testing.T) {
	env := healthyEnv()
	env.GoVersion = ""
	if c := find(Doctor(env), "go toolchain"); c.Level != domain.LevelFail {
		t.Fatalf("missing go must be a failure, got %s", c.Level)
	}
}

func TestDoctorMissingToolIsWarningNotFailure(t *testing.T) {
	env := healthyEnv()
	env.CovdataWorks = false
	r := Doctor(env)
	c := find(r, "go tool covdata")
	if c.Level != domain.LevelWarn {
		t.Fatalf("a missing optional tool must be a warning, got %s", c.Level)
	}
	if !r.Healthy() {
		t.Fatal("a missing optional tool must not make the setup unhealthy")
	}
}

func TestDoctorInstalledButUnconnectedAgentIsWarning(t *testing.T) {
	env := healthyEnv()
	env.AgentConfigs = []AgentConfig{{Agent: "cursor", Installed: true, Registered: false}}
	c := find(Doctor(env), "agent cursor")
	if c.Level != domain.LevelWarn {
		t.Fatalf("installed but unconnected must be a warning, got %s", c.Level)
	}
	if !strings.Contains(c.Fix, "go-anything setup -apply -agent cursor") {
		t.Fatalf("fix must give the exact command: %q", c.Fix)
	}
}

func TestDoctorStaleSkillIsWarning(t *testing.T) {
	env := healthyEnv()
	env.SkillIntact = map[string]bool{"claude": false}
	c := find(Doctor(env), "agent claude")
	if c.Level != domain.LevelWarn {
		t.Fatalf("registered with a stale skill must be a warning, got %s", c.Level)
	}
}

func TestDoctorSkipsAgentsNotInstalled(t *testing.T) {
	env := healthyEnv()
	env.AgentConfigs = []AgentConfig{{Agent: "codex", Installed: false}}
	for _, c := range Doctor(env).Checks {
		if strings.Contains(c.Name, "codex") {
			t.Fatalf("an agent that is not installed must not be reported: %+v", c)
		}
	}
}

func TestDoctorUnwritableReportFolderIsFailure(t *testing.T) {
	env := healthyEnv()
	env.ReportDirOK = false
	if c := find(Doctor(env), "report folder"); c.Level != domain.LevelFail {
		t.Fatalf("an unwritable report folder must be a failure, got %s", c.Level)
	}
}

func TestDoctorBusyPortIsWarning(t *testing.T) {
	env := healthyEnv()
	env.PortAvailable = false
	if c := find(Doctor(env), "webhook receiver port"); c.Level != domain.LevelWarn {
		t.Fatalf("a busy port must be a warning, got %s", c.Level)
	}
}

func TestDoctorNeverChangesEnvironment(t *testing.T) {
	env := healthyEnv()
	before := len(env.AgentConfigs)
	Doctor(env)
	if len(env.AgentConfigs) != before {
		t.Fatal("doctor must not modify its input")
	}
}

func TestDoctorWithoutGoModIsWarningNotFailure(t *testing.T) {
	env := healthyEnv()
	env.RequiredGo = ""
	c := find(Doctor(env), "go toolchain")
	if c.Level != domain.LevelWarn {
		t.Fatalf("an unknown requirement must be a warning, never a failure: %+v", c)
	}
	if strings.Contains(c.Detail, "needs  or newer") {
		t.Fatalf("the detail must not print an empty version: %q", c.Detail)
	}
}
