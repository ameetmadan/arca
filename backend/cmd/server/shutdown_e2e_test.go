package main

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// TestServerProcessExitsCleanlyOnSIGTERM builds the real binary (stub
// providers, no build tags), starts it, sends SIGTERM exactly as
// BackendProcess.swift's Process.terminate() does, and checks that the
// process exits with status 0 within the shutdown budget.
func TestServerProcessExitsCleanlyOnSIGTERM(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping process-level test in -short mode")
	}
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go toolchain not on PATH")
	}

	dir := t.TempDir()
	bin := filepath.Join(dir, "arca-server")
	if out, err := exec.Command(goBin, "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}

	port := freePort(t)
	cfgPath := filepath.Join(dir, "config.yaml")
	cfg := fmt.Sprintf("server:\n  port: %d\nmodels:\n  dir: %s\n", port, filepath.Join(dir, "models"))
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(bin, "--insecure-dev", "-config", cfgPath)
	cmd.Env = append(os.Environ(), "HOME="+dir)
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	t.Cleanup(func() { cmd.Process.Kill() }) //nolint:errcheck

	addr := fmt.Sprintf("127.0.0.1:%d", port)
	deadline := time.Now().Add(10 * time.Second)
	for {
		c, err := net.DialTimeout("tcp", addr, 100*time.Millisecond)
		if err == nil {
			c.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("server did not start listening on %s", addr)
		}
		time.Sleep(50 * time.Millisecond)
	}

	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("signal: %v", err)
	}
	select {
	case err := <-exited:
		if err != nil {
			t.Fatalf("server exited with error after SIGTERM: %v", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("server did not exit after SIGTERM")
	}
}

func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}
