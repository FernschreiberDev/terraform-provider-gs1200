package zyxel

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"
)

// concurrencyWatch records how many requests were ever in flight at once.
type concurrencyWatch struct {
	mu      sync.Mutex
	current int
	peak    int
}

func (w *concurrencyWatch) enter() {
	w.mu.Lock()
	w.current++
	if w.current > w.peak {
		w.peak = w.current
	}
	w.mu.Unlock()
}

func (w *concurrencyWatch) leave() {
	w.mu.Lock()
	w.current--
	w.mu.Unlock()
}

func (w *concurrencyWatch) highest() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.peak
}

// watchedSwitch answers the VLAN table slowly enough that overlapping
// requests are certain to be seen overlapping.
func watchedSwitch(t *testing.T, watch *concurrencyWatch) *Client {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		watch.enter()
		defer watch.leave()
		time.Sleep(40 * time.Millisecond)
		_, _ = io.WriteString(w, realVLANXML)
	}))
	t.Cleanup(server.Close)

	parsed, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	client, err := NewClient(parsed.Host, "s3cret", "http", false, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	return client
}

// TestOneRequestAtATimePerSwitch is the property the hardware forces on us.
//
// A GS1200 answers one request at a time, and each costs it a 1.85 s TLS
// handshake. Four refreshes arriving together do not run in parallel — they
// queue inside the device, and the last one exceeds the client timeout. So
// the queue has to be on this side, where waiting is free.
func TestOneRequestAtATimePerSwitch(t *testing.T) {
	var watch concurrencyWatch
	client := watchedSwitch(t, &watch)

	var wg sync.WaitGroup
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := client.ReadVLANTable(context.Background()); err != nil {
				t.Errorf("ReadVLANTable: %v", err)
			}
		}()
	}
	wg.Wait()

	if peak := watch.highest(); peak != 1 {
		t.Errorf("%d concurrent requests against one switch, want 1", peak)
	}
}

// TestTwoSwitchesRunInParallel is the other half, and the reason the lock is
// keyed by host rather than living on the client: two switches must make
// progress at the same time, or a fleet takes as long as the sum of its parts.
func TestTwoSwitchesRunInParallel(t *testing.T) {
	// One watch shared by both servers: it can only ever see 2 if the two
	// devices really are being talked to at once.
	var shared concurrencyWatch
	first := watchedSwitch(t, &shared)
	second := watchedSwitch(t, &shared)

	var wg sync.WaitGroup
	for _, client := range []*Client{first, second} {
		wg.Add(1)
		go func(c *Client) {
			defer wg.Done()
			for i := 0; i < 3; i++ {
				if _, err := c.ReadVLANTable(context.Background()); err != nil {
					t.Errorf("ReadVLANTable: %v", err)
				}
			}
		}(client)
	}
	wg.Wait()

	if peak := shared.highest(); peak != 2 {
		t.Errorf("peak of %d concurrent requests across two switches, want 2 "+
			"(the lock has to be per device, not global)", peak)
	}
}
