package zyxel

import (
	"context"
	"crypto/tls"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// legacyTLSServer imitates the GS1200's TLS: version 1.2 and one single cipher
// suite, AES128-GCM-SHA256 over an RSA key exchange.
func legacyTLSServer(t *testing.T) *httptest.Server {
	t.Helper()
	server := httptest.NewUnstartedServer(http.HandlerFunc(
		func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(w, realVLANXML)
		}))
	server.TLS = &tls.Config{
		MinVersion:   tls.VersionTLS12,
		MaxVersion:   tls.VersionTLS12,
		CipherSuites: []uint16{tls.TLS_RSA_WITH_AES_128_GCM_SHA256},
	}
	server.StartTLS()
	t.Cleanup(server.Close)
	return server
}

// TestAStockGoClientCannotTalkToThisHardware pins the behaviour that caused
// the failure, so the reason for the explicit cipher list cannot be quietly
// deleted as redundant.
//
// Go 1.22 dropped every RSA-key-exchange suite from the client defaults. The
// switch offers nothing else, so the two share no cipher and the handshake
// dies with a message that never mentions ciphers.
func TestAStockGoClientCannotTalkToThisHardware(t *testing.T) {
	server := legacyTLSServer(t)

	stock := &http.Client{
		Timeout: 5 * time.Second,
		Transport: &http.Transport{
			// Only certificate verification is relaxed — the cipher list is
			// left at Go's default, which is the whole point.
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // deliberate
		},
	}
	resp, err := stock.Get(server.URL)
	if err == nil {
		resp.Body.Close()
		t.Fatal("a stock Go client reached the server; either Go's defaults " +
			"changed back, or this test no longer imitates the hardware")
	}
	if !strings.Contains(err.Error(), "handshake failure") {
		t.Errorf("expected a TLS handshake failure, got %v", err)
	}
}

// TestTheDriverTalksToLegacyTLS is the other half: the same server, reached by
// the client this package builds.
func TestTheDriverTalksToLegacyTLS(t *testing.T) {
	server := legacyTLSServer(t)
	parsed, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}

	client, err := NewClient(parsed.Host, "", "https", false, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}

	entries, err := client.ReadVLANTable(context.Background())
	if err != nil {
		t.Fatalf("the driver could not read over the switch's TLS: %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("got %d VLANs, want 3", len(entries))
	}
}

// TestVerifyTLSStillVerifies guards the obvious mistake of loosening the
// cipher list and losing certificate checking with it.
func TestVerifyTLSStillVerifies(t *testing.T) {
	server := legacyTLSServer(t)
	parsed, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}

	client, err := NewClient(parsed.Host, "", "https", true, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.ReadVLANTable(context.Background()); err == nil {
		t.Fatal("verify_tls = true accepted the test server's self-signed certificate")
	}
}
