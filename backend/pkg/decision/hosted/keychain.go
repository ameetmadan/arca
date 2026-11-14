package hosted

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"
)

// KeySource supplies the API key. The key must never be written to
// config.yaml or to logs (ADR-0008); on macOS it lives in the Keychain.
type KeySource interface {
	APIKey(ctx context.Context) (string, error)
}

// StaticKey is a KeySource holding a fixed key. It is meant for tests.
type StaticKey string

// APIKey implements KeySource.
func (k StaticKey) APIKey(context.Context) (string, error) { return string(k), nil }

// Default Keychain item for the hosted provider. Store the key once with:
//
//	security add-generic-password -s com.arca.hosted-decision -a default -w
//
// (with -w last, security prompts for the value, so it stays out of shell history).
const (
	KeychainService = "com.arca.hosted-decision"
	KeychainAccount = "default"
)

// ErrNoKey means no API key is available, so no request can be made.
var ErrNoKey = errors.New("hosted: no API key in the Keychain")

// Keychain reads a generic password from the macOS login Keychain using the
// system `security` tool (no CGo). On other platforms it always reports
// ErrNoKey. A successfully read key is cached in memory.
type Keychain struct {
	Service string // default KeychainService
	Account string // default KeychainAccount

	// run executes the security tool; nil uses os/exec. Tests replace it.
	run func(ctx context.Context, name string, args ...string) ([]byte, error)
	// goos overrides runtime.GOOS in tests.
	goos string

	mu  sync.Mutex
	key string
}

// APIKey implements KeySource.
func (k *Keychain) APIKey(ctx context.Context) (string, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.key != "" {
		return k.key, nil
	}
	goos := k.goos
	if goos == "" {
		goos = runtime.GOOS
	}
	if goos != "darwin" {
		return "", fmt.Errorf("%w (Keychain is macOS only)", ErrNoKey)
	}
	svc, acct := k.Service, k.Account
	if svc == "" {
		svc = KeychainService
	}
	if acct == "" {
		acct = KeychainAccount
	}
	run := k.run
	if run == nil {
		run = func(ctx context.Context, name string, args ...string) ([]byte, error) {
			ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
			defer cancel()
			return exec.CommandContext(ctx, name, args...).Output()
		}
	}
	out, err := run(ctx, "/usr/bin/security", "find-generic-password", "-s", svc, "-a", acct, "-w")
	if err != nil {
		// The tool's stderr is deliberately not surfaced.
		return "", ErrNoKey
	}
	key := strings.TrimSpace(string(out))
	if key == "" {
		return "", ErrNoKey
	}
	k.key = key
	return key, nil
}
