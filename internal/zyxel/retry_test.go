package zyxel

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

// failFirst fails the first n round trips with err, then delegates. It stands
// in for a pooled connection the switch closed while it sat idle — a race
// that cannot be provoked reliably against a real server.
type failFirst struct {
	inner http.RoundTripper
	err   error
	mu    sync.Mutex
	left  int
	tries int
}

func (f *failFirst) RoundTrip(req *http.Request) (*http.Response, error) {
	f.mu.Lock()
	f.tries++
	if f.left > 0 {
		f.left--
		f.mu.Unlock()
		return nil, f.err
	}
	f.mu.Unlock()
	return f.inner.RoundTrip(req)
}

func clientWithFlakyTransport(t *testing.T, handler http.HandlerFunc, failures int, err error) (*Client, *failFirst) {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	parsed, parseErr := url.Parse(server.URL)
	if parseErr != nil {
		t.Fatal(parseErr)
	}
	client, newErr := NewClient(parsed.Host, "hunter2hunter2", "http", false, 5*time.Second)
	if newErr != nil {
		t.Fatal(newErr)
	}
	flaky := &failFirst{inner: client.http.Transport, err: err, left: failures}
	client.http.Transport = flaky
	return client, flaky
}

// TestALoginSurvivesAStaleConnection is the failure that broke the first
// parallel run: a VLAN read leaves a keep-alive connection in the pool, the
// switch closes it, and the next login picks it up. Go re-sends an idempotent
// request on its own but never a POST — and the login is a POST.
func TestALoginSurvivesAStaleConnection(t *testing.T) {
	var bodies []string
	client, flaky := clientWithFlakyTransport(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(body))
		http.SetCookie(w, &http.Cookie{Name: "Cookies", Value: "TOKEN123="})
		_, _ = io.WriteString(w, "<script>var allow = 1;</script>")
	}, 1, errors.New("http: "+staleConnMessage))

	if err := client.Login(context.Background()); err != nil {
		t.Fatalf("the login should have survived a stale connection: %v", err)
	}
	if flaky.tries != 2 {
		t.Errorf("%d attempts, want 2 (one failure then a success)", flaky.tries)
	}

	// The body has to be re-sent intact: a spent reader would send an empty
	// POST, which the switch reads as a wrong password.
	sum := sha256.Sum256([]byte("hunter2hunter2"))
	want := "password=" + hex.EncodeToString(sum[:])
	if len(bodies) != 1 || bodies[0] != want {
		t.Errorf("body received %q, want %q", bodies, want)
	}
}

// TestARealFailureIsNotRetried keeps the retry narrow. Re-sending on any
// transport error would hide a switch that is genuinely unreachable behind
// three times the wait.
func TestARealFailureIsNotRetried(t *testing.T) {
	client, flaky := clientWithFlakyTransport(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "<script>var allow = 1;</script>")
	}, 1, errors.New("connection refused"))

	err := client.Login(context.Background())
	if err == nil {
		t.Fatal("a real failure must surface, not be retried")
	}
	if !strings.Contains(err.Error(), "connection refused") {
		t.Errorf("unexpected error: %v", err)
	}
	if flaky.tries != 1 {
		t.Errorf("%d attempts, want 1", flaky.tries)
	}
}

// TestAPersistentlyDeadConnectionGivesUp bounds the retry: a switch that
// closes every connection must produce an error, not a loop.
func TestAPersistentlyDeadConnectionGivesUp(t *testing.T) {
	client, flaky := clientWithFlakyTransport(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "<script>var allow = 1;</script>")
	}, 99, errors.New("http: "+staleConnMessage))

	if err := client.Login(context.Background()); err == nil {
		t.Fatal("want a failure once the attempts are exhausted")
	}
	if flaky.tries != staleConnAttempts {
		t.Errorf("%d attempts, want %d", flaky.tries, staleConnAttempts)
	}
}
