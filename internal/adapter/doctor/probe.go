// Package doctor gathers the facts the doctor judges. It only reads: it runs read-only commands,
// looks at files, and binds-then-closes a loopback port. It never writes settings or reports.
package doctor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/go-anything/go-anything/internal/adapter/setup"
	"github.com/go-anything/go-anything/internal/usecase"
)

// probeTimeout bounds each external command so a hung tool cannot hang the doctor.
const probeTimeout = 20 * time.Second

var goModGo = regexp.MustCompile(`(?m)^go\s+(\S+)`)

// Gather reads the machine and returns what the doctor needs. reportRoot is the project folder
// where reports are written; home is the user's home folder for agent settings.
func Gather(moduleRoot, reportRoot, home string) usecase.Environment {
	env := usecase.Environment{
		RequiredGo:  requiredGo(moduleRoot),
		SkillIntact: map[string]bool{},
	}
	env.GoVersion = goVersion()
	env.CovdataWorks = runs("go", "tool", "covdata", "-h")
	env.PprofWorks = runs("go", "tool", "pprof", "-h")
	env.ReportDirOK = writable(filepath.Join(reportRoot, ".go-anything"))
	env.PortAvailable = loopbackPortFree()
	for _, a := range setup.Agents {
		ac := agentState(home, a)
		env.AgentConfigs = append(env.AgentConfigs, ac)
		env.SkillIntact[a.Name] = skillMatches(home, a.Name)
	}
	return env
}

func requiredGo(moduleRoot string) string {
	data, err := os.ReadFile(filepath.Join(moduleRoot, "go.mod"))
	if err != nil {
		return ""
	}
	if m := goModGo.FindSubmatch(data); m != nil {
		return string(m[1])
	}
	return ""
}

// goVersion returns `go version` output, or empty when go is not runnable.
func goVersion() string {
	ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, "go", "version").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// runs reports whether a command is available. `go tool X -h` may exit non-zero on some Go
// versions while still proving the tool exists, so usage text also counts as available.
func runs(name string, args ...string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
	defer cancel()
	var buf bytes.Buffer
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err := cmd.Run()
	if err == nil {
		return true
	}
	var ee *exec.ExitError
	return errors.As(err, &ee) && strings.Contains(strings.ToLower(buf.String()), "usage")
}

// writable checks that dir can be created and a file written inside it, then removes the probe.
func writable(dir string) bool {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return false
	}
	probe, err := os.CreateTemp(dir, ".doctor-*")
	if err != nil {
		return false
	}
	name := probe.Name()
	probe.Close()
	return os.Remove(name) == nil
}

// loopbackPortFree binds an ephemeral loopback port and releases it immediately.
func loopbackPortFree() bool {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return false
	}
	return ln.Close() == nil
}

func agentState(home string, a setup.Agent) usecase.AgentConfig {
	ac := usecase.AgentConfig{Agent: a.Name}
	if fi, err := os.Stat(filepath.Join(home, a.Marker)); err == nil && fi.IsDir() {
		ac.Installed = true
	}
	path := filepath.Join(home, a.Path)
	data, err := os.ReadFile(path)
	if err != nil {
		return ac
	}
	ac.FileExists = true
	ac.Registered = registered(data, a)
	return ac
}

// registered reports whether our server entry is present in the agent's settings file.
func registered(data []byte, a setup.Agent) bool {
	switch a.Format {
	case setup.FormatMCPServers:
		var doc map[string]any
		if json.Unmarshal(data, &doc) != nil {
			return false
		}
		servers, _ := doc["mcpServers"].(map[string]any)
		_, ok := servers[setup.ServerName]
		return ok
	case setup.FormatOpenCode:
		var doc map[string]any
		if json.Unmarshal(data, &doc) != nil {
			return false
		}
		mcp, _ := doc["mcp"].(map[string]any)
		servers, _ := mcp["servers"].(map[string]any)
		_, ok := servers[setup.ServerName]
		return ok
	case setup.FormatCodexTOML:
		return strings.Contains(string(data), "[mcp_servers."+setup.ServerName+"]")
	}
	return false
}

// skillMatches reports whether the agent's installed skill equals the embedded copy.
func skillMatches(home, agent string) bool {
	path := setup.SkillPath(home, agent)
	if path == "" {
		return true // this agent has no skill folder, so there is nothing to be out of date
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	return string(data) == setup.SkillMD
}
