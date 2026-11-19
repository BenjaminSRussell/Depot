package main

import (
	"bufio"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const (
	conversationsPath = "/api/v1/agents/copilot/conversations"
	messagesPath      = "/api/v1/agents/copilot/conversations/c1/messages"
	bearer            = "Bearer abc.def.ghi"
	streamDeadline    = 5 * time.Second
)

func sseFrame(event, data string) string { return "event: " + event + "\ndata: " + data + "\n\n" }

func agentsUpstream(t *testing.T, h http.HandlerFunc) *url.URL {
	t.Helper()
	up := httptest.NewServer(h)
	t.Cleanup(up.Close)
	u, _ := url.Parse(up.URL)
	return u
}

func shippedProcesses(t *testing.T) (Config, []Process) {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "conf.d"), 0o750); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join("..", "..", "deploy", "windows", "features", "node.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "conf.d", "node.yaml"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(filepath.Join(dir, "depot.yaml"), map[string]string{"FEATURE_NODE": "1", "PG_DSN": "postgres://o:p@db:5432/postgres"})
	if err != nil {
		t.Fatal(err)
	}
	cfg.InstallDir, cfg.DataDir = dir, filepath.Join(dir, "data")
	_, procs, err := cfg.Resolve(func(string) string { return "" }, func(string) ([]string, error) { return nil, nil }, ModeServe)
	if err != nil {
		t.Fatal(err)
	}
	return cfg, procs
}

func agentsFront(t *testing.T, target *url.URL, guard Guard) *httptest.Server {
	t.Helper()
	if guard == nil {
		guard = func(next http.Handler) http.Handler { return next }
	}
	cfg, procs := shippedProcesses(t)
	port, err := strconv.Atoi(target.Port())
	if err != nil {
		t.Fatal(err)
	}
	for i := range procs {
		if procs[i].Route == "agents" {
			procs[i].Port = port
		}
	}
	front := httptest.NewServer(NewGateway(guard, upstreamsOf(procs), nil, func() (bool, []Status) { return true, nil }, "gw-secret", cfg.Features, forwardAuthOf(procs)))
	t.Cleanup(front.Close)
	return front
}

func TestShippedConfig_RoutesTheAgentsPrefixWithTheAPIPrefixRestored(t *testing.T) {
	_, procs := shippedProcesses(t)
	upstreams := upstreamsOf(procs)
	if upstreams["agents"] == nil || upstreams["agents"].Path != "/api" || !forwardAuthOf(procs)["agents"] {
		t.Fatalf("upstreams %v forwardAuth %v", upstreams, forwardAuthOf(procs))
	}
}

func agentsPost(ctx context.Context, t *testing.T, client *http.Client, url, method string) *http.Response {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, method, url, strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", bearer)
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	return resp
}

func readFrame(t *testing.T, br *bufio.Reader) string {
	t.Helper()
	var frame strings.Builder
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			t.Fatalf("stream ended inside a frame %q: %v", frame.String(), err)
		}
		frame.WriteString(line)
		if line == "\n" {
			return frame.String()
		}
	}
}

func TestAgentsRoute_RoutesEveryTicketPath(t *testing.T) {
	type seenRequest struct{ method, path, authorization string }
	var seen atomic.Pointer[seenRequest]
	target := agentsUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		seen.Store(&seenRequest{r.Method, r.URL.Path, r.Header.Get("Authorization")})
		w.WriteHeader(http.StatusNoContent)
	})
	front := agentsFront(t, target, nil)
	calls := []struct{ method, rest string }{
		{"POST", "/copilot/conversations"},
		{"POST", "/copilot/conversations/c1/messages"},
		{"GET", "/budgets"},
		{"GET", "/definitions"},
		{"GET", "/availability"},
		{"GET", "/approvals"},
		{"GET", "/runs"},
		{"POST", "/runs/r1/actions/a1/decision"},
		{"POST", "/runs/r1/actions/a1:undo"},
		{"GET", "/briefings"},
		{"POST", "/briefings:generate"},
		{"POST", "/briefings/b1:mark-read"},
	}
	for _, c := range calls {
		t.Run(c.method+" "+c.rest, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), streamDeadline)
			defer cancel()
			resp := agentsPost(ctx, t, front.Client(), front.URL+"/api/v1/agents"+c.rest, c.method)
			_ = resp.Body.Close()
			got := seen.Load()
			if resp.StatusCode != http.StatusNoContent || got == nil {
				t.Fatalf("status %d, upstream saw %v", resp.StatusCode, got)
			}
			if got.method != c.method || got.path != "/api/v1/agents"+c.rest || got.authorization != bearer {
				t.Fatalf("upstream saw %+v", *got)
			}
			seen.Store(nil)
		})
	}
}

func TestAgentsRoute_StreamsSSEWithoutBuffering(t *testing.T) {
	release := make(chan struct{})
	target := agentsUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, sseFrame("token", `{"text":"one"}`))
		w.(http.Flusher).Flush()
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		for _, f := range []string{sseFrame("token", `{"text":"two"}`), sseFrame("token", `{"text":"three"}`), sseFrame("done", `{}`)} {
			_, _ = io.WriteString(w, f)
			w.(http.Flusher).Flush()
		}
	})
	front := agentsFront(t, target, nil)
	ctx, cancel := context.WithTimeout(context.Background(), streamDeadline)
	defer cancel()
	resp := agentsPost(ctx, t, front.Client(), front.URL+messagesPath, "POST")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("status %d content-type %q", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	br := bufio.NewReader(resp.Body)
	if got := readFrame(t, br); got != sseFrame("token", `{"text":"one"}`) {
		t.Fatalf("first frame = %q", got)
	}
	close(release)
	for _, want := range []string{sseFrame("token", `{"text":"two"}`), sseFrame("token", `{"text":"three"}`), sseFrame("done", `{}`)} {
		if got := readFrame(t, br); got != want {
			t.Fatalf("frame = %q, want %q", got, want)
		}
	}
	if rest, err := io.ReadAll(br); err != nil || len(rest) != 0 {
		t.Fatalf("stream did not end cleanly: %q %v", rest, err)
	}
}

func TestAgentsRoute_PassesRefusalsAndErrorsThrough(t *testing.T) {
	type refusal struct {
		name   string
		status int
		code   string
	}
	cases := []refusal{
		{"AGT-0202 refusal", http.StatusUnprocessableEntity, "AGT-0202"},
		{"AGT-0203 refusal", http.StatusForbidden, "AGT-0203"},
		{"401", http.StatusUnauthorized, "AUTH-0001"},
		{"403", http.StatusForbidden, "AUTH-0003"},
		{"500", http.StatusInternalServerError, "AGT-0500"},
		{"503", http.StatusServiceUnavailable, "AGT-0503"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			const contentType = "application/problem+json"
			body := `{"type":"https://depot.dev/problems/` + c.code + `","status":` + strconv.Itoa(c.status) + `,"code":"` + c.code + `"}`
			target := agentsUpstream(t, func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", contentType)
				w.WriteHeader(c.status)
				_, _ = io.WriteString(w, body)
			})
			front := agentsFront(t, target, nil)
			ctx, cancel := context.WithTimeout(context.Background(), streamDeadline)
			defer cancel()
			resp := agentsPost(ctx, t, front.Client(), front.URL+messagesPath, "POST")
			defer resp.Body.Close()
			got, err := io.ReadAll(resp.Body)
			if err != nil || resp.StatusCode != c.status || resp.Header.Get("Content-Type") != contentType || string(got) != body {
				t.Fatalf("got %d %q %q (%v), want %d %q %q", resp.StatusCode, resp.Header.Get("Content-Type"), got, err, c.status, contentType, body)
			}
		})
	}

	t.Run("stream that fails after the first frame", func(t *testing.T) {
		want := sseFrame("token", `{"text":"one"}`) + sseFrame("error", `{"code":"AGT-0206"}`)
		target := agentsUpstream(t, func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, sseFrame("token", `{"text":"one"}`))
			w.(http.Flusher).Flush()
			_, _ = io.WriteString(w, sseFrame("error", `{"code":"AGT-0206"}`))
		})
		front := agentsFront(t, target, nil)
		ctx, cancel := context.WithTimeout(context.Background(), streamDeadline)
		defer cancel()
		resp := agentsPost(ctx, t, front.Client(), front.URL+messagesPath, "POST")
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		if err != nil || resp.StatusCode != http.StatusOK || string(body) != want {
			t.Fatalf("got %d %q (%v), want %q", resp.StatusCode, body, err, want)
		}
	})

	t.Run("upstream unreachable", func(t *testing.T) {
		down := httptest.NewServer(http.NotFoundHandler())
		target, _ := url.Parse(down.URL)
		down.Close()
		front := agentsFront(t, target, nil)
		ctx, cancel := context.WithTimeout(context.Background(), streamDeadline)
		defer cancel()
		resp := agentsPost(ctx, t, front.Client(), front.URL+conversationsPath, "POST")
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != http.StatusBadGateway || !strings.Contains(string(body), "GW-0502") {
			t.Fatalf("got %d %s", resp.StatusCode, body)
		}
	})
}

func TestAgentsRoute_AGuardRefusalIsNotProxied(t *testing.T) {
	reject := func(status int, body string) Guard {
		return func(http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/problem+json")
				w.WriteHeader(status)
				_, _ = io.WriteString(w, body)
			})
		}
	}
	cases := []struct {
		name   string
		guard  Guard
		status int
	}{
		{"no token", reject(http.StatusUnauthorized, `{"code":"AUTH-0001"}`), http.StatusUnauthorized},
		{"token without the copilot duty", reject(http.StatusForbidden, `{"code":"AUTH-0003","errors":{"duty_missing":"mcp.copilot.use"}}`), http.StatusForbidden},
	}
	for _, c := range cases {
		for _, p := range []string{conversationsPath, messagesPath} {
			t.Run(c.name+" "+p, func(t *testing.T) {
				var hits atomic.Int32
				target := agentsUpstream(t, func(http.ResponseWriter, *http.Request) { hits.Add(1) })
				front := agentsFront(t, target, c.guard)
				ctx, cancel := context.WithTimeout(context.Background(), streamDeadline)
				defer cancel()
				resp := agentsPost(ctx, t, front.Client(), front.URL+p, "POST")
				_ = resp.Body.Close()
				if resp.StatusCode != c.status || hits.Load() != 0 {
					t.Fatalf("status %d, upstream hits %d", resp.StatusCode, hits.Load())
				}
			})
		}
	}
}

func TestAgentsRoute_ClientDisconnectCancelsUpstream(t *testing.T) {
	cancelled := make(chan struct{})
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, sseFrame("token", `{"text":"one"}`))
		w.(http.Flusher).Flush()
		<-r.Context().Done()
		close(cancelled)
	}))
	target, _ := url.Parse(up.URL)
	front := agentsFront(t, target, nil)
	client := &http.Client{Transport: &http.Transport{}}

	ctx, cancel := context.WithTimeout(context.Background(), streamDeadline)
	defer cancel()
	resp := agentsPost(ctx, t, client, front.URL+messagesPath, "POST")
	if got := readFrame(t, bufio.NewReader(resp.Body)); got != sseFrame("token", `{"text":"one"}`) {
		t.Fatalf("first frame = %q", got)
	}
	_ = resp.Body.Close()

	select {
	case <-cancelled:
	case <-time.After(streamDeadline):
		t.Fatal("upstream request was not cancelled after the client disconnected")
	}
	client.CloseIdleConnections()
	up.Close()
}
