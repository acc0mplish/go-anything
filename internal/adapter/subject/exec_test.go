package subject

import (
	"strings"
	"testing"
	"time"
)

func TestExecEchoesStdin(t *testing.T) {
	out, err := Exec{Argv: []string{"cat"}}.Run("hello")
	if err != nil || out != "hello" {
		t.Fatalf("got %q err=%v", out, err)
	}
}

func TestExecNoCommand(t *testing.T) {
	if _, err := (Exec{}).Run("x"); err != ErrNoCommand {
		t.Fatalf("want ErrNoCommand, got %v", err)
	}
}

func TestExecNonZeroExitIsError(t *testing.T) {
	_, err := Exec{Argv: []string{"sh", "-c", "echo bad >&2; exit 3"}}.Run("")
	if err == nil || !strings.Contains(err.Error(), "bad") {
		t.Fatalf("want stderr in error, got %v", err)
	}
}

func TestExecTimeout(t *testing.T) {
	_, err := Exec{Argv: []string{"sleep", "5"}, Timeout: 100 * time.Millisecond}.Run("")
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("want timeout, got %v", err)
	}
}

func TestExecDoesNotInterpretShell(t *testing.T) {
	out, err := Exec{Argv: []string{"cat"}}.Run("$(echo INJECTED)")
	if err != nil || out != "$(echo INJECTED)" {
		t.Fatalf("input must pass through literally, got %q err=%v", out, err)
	}
}
