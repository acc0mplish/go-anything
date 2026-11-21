package wiregen

import (
	"bytes"
	"context"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// BuildApp builds the program in the module root (its main package) into out.
func BuildApp(root, out string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "go", "build", "-o", out, ".")
	cmd.Dir = root
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("wire verify: build the program: %w\n%s", err, buf.String())
	}
	return nil
}

// ListenAddress reads the address literal of the program's http.ListenAndServe call, so the program can
// be started and reached. A non-literal address cannot be read, and the caller is told so.
func ListenAddress(root, mainFile string) (string, error) {
	src, err := os.ReadFile(filepath.Join(root, mainFile))
	if err != nil {
		return "", err
	}
	f, err := parser.ParseFile(token.NewFileSet(), mainFile, src, 0)
	if err != nil {
		return "", err
	}
	var addr string
	var found bool
	ast.Inspect(f, func(n ast.Node) bool {
		c, ok := n.(*ast.CallExpr)
		if !ok || found {
			return !found
		}
		sel, ok := c.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "ListenAndServe" || len(c.Args) < 1 {
			return true
		}
		found = true
		if lit, ok := c.Args[0].(*ast.BasicLit); ok && lit.Kind == token.STRING {
			addr, _ = strconv.Unquote(lit.Value)
		}
		return false
	})
	if !found {
		return "", fmt.Errorf("wire verify: no http.ListenAndServe call in %s", mainFile)
	}
	if addr == "" {
		return "", fmt.Errorf("wire verify: the listen address is not a literal in %s; start the program by hand and use wire replay", mainFile)
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return "", fmt.Errorf("wire verify: listen address %q: %w", addr, err)
	}
	if host == "" || host == "0.0.0.0" {
		host = "127.0.0.1"
	}
	return net.JoinHostPort(host, port), nil
}

// StartApp starts the built program with extra environment and waits until its address accepts
// connections. The caller stops it with the returned function.
func StartApp(bin string, env []string, addr string) (func(), error) {
	cmd := exec.Command(bin)
	cmd.Env = append(os.Environ(), env...)
	var logBuf bytes.Buffer
	cmd.Stdout = &logBuf
	cmd.Stderr = &logBuf
	if err := cmd.Start(); err != nil {
		return func() {}, fmt.Errorf("wire verify: start program: %w", err)
	}
	stop := func() {
		cmd.Process.Kill()
		cmd.Wait()
	}
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if conn, err := net.DialTimeout("tcp", addr, 200*time.Millisecond); err == nil {
			conn.Close()
			return stop, nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	stop()
	return func() {}, fmt.Errorf("wire verify: the program did not listen on %s within 15s; output:\n%s", addr, logBuf.String())
}

// unitEnv is the environment that sends one unit to its rewrite and the rest to the original.
func unitEnv(unit string, originalCLI, originalURL string) []string {
	env := []string{"GA_UNIT_" + strings.ToUpper(unit) + "=rewrite"}
	if originalCLI != "" {
		env = append(env, "GA_ORIGINAL_CLI="+originalCLI)
	}
	if originalURL != "" {
		env = append(env, "GA_ORIGINAL_URL="+originalURL)
	}
	return env
}

// UnitEnv exposes unitEnv for the command.
func UnitEnv(unit, originalCLI, originalURL string) []string {
	return unitEnv(unit, originalCLI, originalURL)
}
