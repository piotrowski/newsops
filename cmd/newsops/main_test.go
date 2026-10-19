package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestCommandsRouting(t *testing.T) {
	for name, fn := range commands {
		if fn == nil {
			t.Errorf("command %q has nil handler", name)
		}
	}

	expected := []string{
		"status", "sources:check", "fetch", "dedupe", "candidates",
		"collect", "query", "decide", "show", "version",
	}
	for _, want := range expected {
		if _, ok := commands[want]; !ok {
			t.Errorf("missing command %q in routing table", want)
		}
	}
}

func TestRunCommand_Version(t *testing.T) {
	ctx := context.Background()
	err := runCommand(ctx, cmdVersion, nil)
	if err != nil {
		t.Fatalf("cmdVersion returned error: %v", err)
	}
}

func TestRunCommand_Stub(t *testing.T) {
	ctx := context.Background()
	for name, fn := range commands {
		if name == "version" || name == "status" {
			continue
		}
		err := runCommand(ctx, fn, nil)
		if err == nil {
			t.Errorf("command %q should return error", name)
		}
		if e, ok := err.(interface{ ExitCode() int }); !ok || e.ExitCode() != 2 {
			t.Errorf("command %q: expected exit code 2, got %v", name, err)
		}
	}
}

func TestRunCommand_PlainError(t *testing.T) {
	fake := func(context.Context, []string) (any, error) {
		return nil, assertError("boom")
	}
	err := runCommand(context.Background(), fake, nil)
	if err == nil {
		t.Fatal("expected error from plain error command")
	}
	if e, ok := err.(interface{ ExitCode() int }); !ok || e.ExitCode() != 2 {
		t.Errorf("expected exit code 2 for plain error, got %v", err)
	}
}

func TestRunCommand_NilResult(t *testing.T) {
	fake := func(context.Context, []string) (any, error) {
		return nil, nil
	}
	err := runCommand(context.Background(), fake, nil)
	if err != nil {
		t.Fatalf("nil result should not produce error: %v", err)
	}
}

func TestExitCodeMapping(t *testing.T) {
	tests := []struct {
		result any
		err    error
		want   int
	}{
		{&ExitError{Code: 1, Slug: "partial"}, nil, 1},
		{&ExitError{Code: 2, Slug: "fail"}, nil, 2},
		{nil, assertError("oops"), 2},
		{nil, nil, 0},
	}
	for _, tt := range tests {
		ctx := context.Background()
		fn := func(context.Context, []string) (any, error) {
			return tt.result, tt.err
		}
		err := runCommand(ctx, fn, nil)
		if tt.want == 0 {
			if err != nil {
				t.Errorf("want exit code 0 for result=%v err=%v, got %v", tt.result, tt.err, err)
			}
			continue
		}
		if e, ok := err.(interface{ ExitCode() int }); !ok || e.ExitCode() != tt.want {
			t.Errorf("want exit code %d for result=%v err=%v, got %v", tt.want, tt.result, tt.err, err)
		}
	}
}

func TestErrorEnvelopeKeys(t *testing.T) {
	envelope := buildEnvelope("test_slug", "test message", nil)
	keys := make([]string, 0, len(envelope))
	for k := range envelope {
		keys = append(keys, k)
	}
	if len(keys) != 2 {
		t.Fatalf("expected 2 keys, got %d: %v", len(keys), keys)
	}
	if _, ok := envelope["error"]; !ok {
		t.Error("envelope missing key 'error'")
	}
	if _, ok := envelope["message"]; !ok {
		t.Error("envelope missing key 'message'")
	}

	envelope2 := buildEnvelope("slug", "msg", map[string]any{"extra": true})
	if len(envelope2) != 3 {
		t.Fatalf("expected 3 keys with fields, got %d", len(envelope2))
	}
}

func TestExitError_Error(t *testing.T) {
	e := ExitError{Message: "something broke"}
	if e.Error() != "something broke" {
		t.Errorf("ExitError.Error() = %q, want %q", e.Error(), "something broke")
	}
}

func TestCommandFlagsRegistered(t *testing.T) {
	for name := range commandFlags {
		if _, ok := commands[name]; !ok {
			t.Errorf("flags registered for unknown command %q", name)
		}
	}
}

// --- integration tests via the compiled binary ---

func TestIntegration_NoArgs(t *testing.T) {
	cmd := exec.Command(binPath())
	stdout, stderr, err := captureOutput(cmd)
	if err == nil {
		t.Fatal("expected non-zero exit for no args")
	}
	if exitErr, ok := err.(*exec.ExitError); !ok || exitErr.ExitCode() != 2 {
		t.Fatalf("expected exit code 2, got %v", err)
	}
	if !strings.Contains(stdout, "status") && !strings.Contains(stderr, "status") {
		t.Error("output should list subcommands")
	}
}

func TestIntegration_UnknownCommand(t *testing.T) {
	cmd := exec.Command(binPath(), "nonsense")
	stdout, stderr, err := captureOutput(cmd)
	if err == nil {
		t.Fatal("expected non-zero exit for unknown command")
	}
	if exitErr, ok := err.(*exec.ExitError); !ok || exitErr.ExitCode() != 2 {
		t.Fatalf("expected exit code 2, got %v", err)
	}
	// Without --json, error text goes to stderr.
	if !strings.Contains(stderr, "unknown_command") {
		t.Errorf("stderr should contain unknown_command: %s", stderr)
	}
	// stdout should be empty (no JSON).
	if len(strings.TrimSpace(stdout)) > 0 {
		t.Errorf("stdout should be empty without --json, got: %q", stdout)
	}
}

func TestIntegration_UnknownCommand_JSON(t *testing.T) {
	cmd := exec.Command(binPath(), "nonsense", "--json")
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatal("expected non-zero exit for unknown command")
	}
	if exitErr, ok := err.(*exec.ExitError); !ok || exitErr.ExitCode() != 2 {
		t.Fatalf("expected exit code 2, got %v", err)
	}

	var envelope map[string]any
	if err := json.Unmarshal(out, &envelope); err != nil {
		t.Fatalf("output is not valid JSON: %s\nerr: %v", string(out), err)
	}
	if slug, ok := envelope["error"].(string); !ok || slug != "unknown_command" {
		t.Errorf("expected error=unknown_command, got %v", envelope["error"])
	}
}

func TestIntegration_Version(t *testing.T) {
	cmd := exec.Command(binPath(), "version", "--json")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("unexpected error: %v\noutput: %s", err, string(out))
	}

	var result map[string]string
	if err := json.Unmarshal(out, &result); err != nil {
		t.Fatalf("output is not valid JSON: %s\nerr: %v", string(out), err)
	}
	if _, ok := result["version"]; !ok {
		t.Error("missing 'version' key in version output")
	}
	if _, ok := result["commit"]; !ok {
		t.Error("missing 'commit' key in version output")
	}
}

func TestIntegration_SubcommandJSON(t *testing.T) {
	cmd := exec.Command(binPath(), "status", "--json")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("unexpected error: %v\noutput: %s", err, string(out))
	}

	var result map[string]any
	if err := json.Unmarshal(out, &result); err != nil {
		t.Fatalf("output is not valid JSON: %s\nerr: %v", string(out), err)
	}

	// Verify top-level keys exist.
	for _, key := range []string{"stages", "counts", "digest"} {
		if _, ok := result[key]; !ok {
			t.Errorf("missing key %q in status output", key)
		}
	}

	// Verify stages has the three expected stages.
	stages, ok := result["stages"].(map[string]any)
	if !ok {
		t.Fatal("stages should be a JSON object")
	}
	for _, stageName := range []string{"fetch", "dedupe", "candidates"} {
		if _, ok := stages[stageName]; !ok {
			t.Errorf("missing stage %q in status output", stageName)
		}
	}
}

// captureOutput runs cmd with separate stdout/stderr buffers.
func captureOutput(cmd *exec.Cmd) (stdout, stderr string, err error) {
	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	err = cmd.Run()
	return outBuf.String(), errBuf.String(), err
}

// binPath returns the path to the newsops binary for integration tests.
// binaryPath is the newsops binary built by TestMain. Integration tests used
// to run ../../newsops, which silently tested whatever stale build happened
// to be lying in the repo root.
var binaryPath string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "newsops-test-bin")
	if err != nil {
		fmt.Fprintln(os.Stderr, "tempdir:", err)
		os.Exit(1)
	}
	binaryPath = filepath.Join(dir, "newsops")
	build := exec.Command("go", "build", "-o", binaryPath, ".")
	build.Stderr = os.Stderr
	if err := build.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "build test binary:", err)
		os.Exit(1)
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

func binPath() string { return binaryPath }

type assertError string

func (e assertError) Error() string { return string(e) }
