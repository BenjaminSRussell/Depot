package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"

	"github.com/BenjaminSRussell/depot/platform/logpseudo"
)

// ExecRunner starts real child processes. Their stdout and stderr go to Logs.
type ExecRunner struct {
	Logs func(name string) io.Writer
}

type execHandle struct {
	cmd  *exec.Cmd
	done chan struct{}
	err  error
}

// Start launches p with its environment on top of the parent's (childEnv).
func (r ExecRunner) Start(ctx context.Context, p Process) (Handle, error) {
	cmd := exec.Command(p.Exe, p.Args...) //nolint:gosec // the executable comes from the installed bin directory or the admin-owned config
	cmd.Env = childEnv(os.Environ(), p)
	cmd.WaitDelay = 5 * time.Second // a grandchild holding the pipes open must not block Wait forever
	setProcAttr(cmd)
	var logw io.Writer
	if r.Logs != nil {
		logw = r.Logs(p.Name)
		cmd.Stdout, cmd.Stderr = logw, logw
	}
	if err := cmd.Start(); err != nil {
		closeWriter(logw)
		return nil, fmt.Errorf("start %s: %w", p.Name, err)
	}
	attachToJob(cmd)
	h := &execHandle{cmd: cmd, done: make(chan struct{})}
	go func() {
		h.err = cmd.Wait()
		closeWriter(logw) // each start opens its own log file handle; release it when the child is gone
		close(h.done)
	}()
	return h, nil
}

func (h *execHandle) Wait() error { <-h.done; return h.err }

// fieldcryptServices are the services that load field-encryption keys at start (fieldcrypt.FromEnv in their cmd: hr refuses
// to start without them, party and sales lose bank accounts and one-time customers). They inherit the supervisor's
// FIELDCRYPT_* variables, as every child did before B-04; no other child does. TestFieldcryptServicesAreTheOnesThatLoadKeys
// keeps the list equal to the services that call fieldcrypt.FromEnv. docs/ops/windows-install.md documents it.
var fieldcryptServices = map[string]bool{
	"compliance": true, "hr": true, "migration": true, "party": true, "payables": true, "receiving": true, "sales": true,
}

// childEnv is p's environment: the supervisor's, without its keys (keyVariable), then p.Env in key order. A key reaches a
// child only when that child's own configuration sets it, or, for the field-encryption keys, when the child is one of
// fieldcryptServices: an operator who gives depot-server a pseudonymization key for the gateway's records, or
// field-encryption keys, does not hand them to every one of the 60-plus services it starts.
func childEnv(parent []string, p Process) []string {
	env := make([]string, 0, len(parent)+len(p.Env))
	for _, kv := range parent {
		name, _, _ := strings.Cut(kv, "=")
		if !keyVariable(name) || (fieldcryptVariable(name) && fieldcryptServices[p.Name]) {
			env = append(env, kv)
		}
	}
	keys := make([]string, 0, len(p.Env))
	for k := range p.Env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		env = append(env, k+"="+p.Env[k])
	}
	return env
}

// subjectKeyEnv is getenv without the field-encryption variables, for logpseudo.LoadSubjectKey: the gateway's records are
// pseudonymized under the dedicated pseudonymization key only, so nothing in depot-server asks an operator to give the
// supervisor fieldcrypt's master keys.
func subjectKeyEnv(getenv func(string) string) func(string) string {
	return func(name string) string {
		if strings.HasPrefix(strings.ToUpper(name), "FIELDCRYPT_") {
			return ""
		}
		return getenv(name)
	}
}

// keyVariable reports whether an environment variable carries key material the supervisor must not pass on: the
// field-encryption master keys (FIELDCRYPT_*) and the log pseudonymization key (logpseudo.EnvKey, EnvKeyFile). Windows
// variable names are case-insensitive, so the check is too.
func keyVariable(name string) bool {
	upper := strings.ToUpper(name)
	return fieldcryptVariable(name) || upper == logpseudo.EnvKey || upper == logpseudo.EnvKeyFile
}

// fieldcryptVariable reports whether a variable configures the field-encryption keys (FIELDCRYPT_KEYS, _KEYS_FILE,
// _CURRENT_KEY_ID), case-insensitively.
func fieldcryptVariable(name string) bool { return strings.HasPrefix(strings.ToUpper(name), "FIELDCRYPT_") }

func closeWriter(w io.Writer) {
	if c, ok := w.(io.Closer); ok {
		_ = c.Close()
	}
}

// Stop asks the child to stop with an interrupt and kills it after grace. Windows has no portable way to deliver a graceful signal to a
// child of a service (no console to send CTRL_BREAK on), so there the child is killed at once; the services commit their work in
// database transactions and replay from the outbox, so a hard stop loses nothing that was acknowledged.
func (h *execHandle) Stop(grace time.Duration) error {
	if err := interrupt(h.cmd.Process); err != nil {
		grace = 0
	}
	select {
	case <-h.done:
		return nil
	case <-time.After(grace):
	}
	if err := h.cmd.Process.Kill(); err != nil && !isDone(err) {
		return err
	}
	<-h.done
	return nil
}

func isDone(err error) bool { return err == os.ErrProcessDone }

// ReadyProbe checks a child's admin /readyz (or the ReadyPath its spec names).
func ReadyProbe(client *http.Client) Prober {
	return func(ctx context.Context, p Process) error {
		path := p.ReadyPath
		if path == "" {
			path = "/readyz"
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("http://127.0.0.1:%d%s", p.AdminPort, path), nil)
		if err != nil {
			return err
		}
		resp, err := client.Do(req)
		if err != nil {
			return err
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("%s returned %d", path, resp.StatusCode)
		}
		return nil
	}
}
