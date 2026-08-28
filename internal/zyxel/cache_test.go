package zyxel

import (
	"context"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/FernschreiberDev/terraform-provider-gs1200/internal/fakeswitch"
)

// newSeededFake is an emulated GS1200 carrying the configuration captured
// from the real 192.0.2.10.
func newSeededFake(t *testing.T) *fakeswitch.Switch {
	t.Helper()
	return fakeswitch.New("s3cret")
}

// clientForFake points a real client at an emulated GS1200.
func clientForFake(t *testing.T, device *fakeswitch.Switch, password string) *Client {
	t.Helper()
	server := httptest.NewServer(device.Handler())
	t.Cleanup(server.Close)
	parsed, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	client, err := NewClient(parsed.Host, password, "http", false, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func countLogins(device *fakeswitch.Switch) int {
	count := 0
	for _, path := range device.Calls {
		if path == "/logon.cgi" {
			count++
		}
	}
	return count
}

func TestRepeatedReadsClaimTheSessionOnce(t *testing.T) {
	// Every login locks the owner out of their own switch. A five-port switch
	// has five PVID resources, and Terraform refreshes each one; without the
	// cache that is five login/logout cycles per plan, per switch.
	device := fakeswitch.New("s3cret")
	client := clientForFake(t, device, "s3cret")

	for i := 0; i < 5; i++ {
		if _, err := client.ReadConfig(context.Background()); err != nil {
			t.Fatalf("read %d: %v", i, err)
		}
	}
	if got := countLogins(device); got != 1 {
		t.Errorf("five reads caused %d logins, want 1", got)
	}
	if device.SessionHeld() {
		t.Error("the session was never released")
	}
}

func TestAWriteInvalidatesTheCache(t *testing.T) {
	// A cache that survived a write would report the configuration the write
	// replaced, and Terraform would record that as the new state.
	device := fakeswitch.New("s3cret")
	client := clientForFake(t, device, "s3cret")
	ctx := context.Background()

	if _, err := client.ReadConfig(ctx); err != nil {
		t.Fatalf("first read: %v", err)
	}
	if _, err := client.WriteVLAN(ctx, VLANEntry{VID: 20, Tagged: []int{1}}, false); err != nil {
		t.Fatalf("write: %v", err)
	}

	after, err := client.ReadConfig(ctx)
	if err != nil {
		t.Fatalf("read after write: %v", err)
	}
	if _, ok := after.VLAN(20); !ok {
		t.Error("the read after a write returned the configuration the write replaced")
	}
}

func TestTheDriverAlwaysReleasesTheSession(t *testing.T) {
	// The failure this guards against is silent: the write is refused, the
	// error is clear, and the switch stays locked until it times out.
	device := fakeswitch.New("s3cret")
	client := clientForFake(t, device, "s3cret")
	ctx := context.Background()

	// Removing port 1 from the management VLAN is refused by the guard, well
	// after the session has been claimed.
	_, err := client.WriteVLAN(ctx, VLANEntry{VID: 1, Untagged: []int{2}}, false)
	if err == nil {
		t.Fatal("expected the guard to refuse this")
	}
	if device.SessionHeld() {
		t.Error("a refused write left the switch locked")
	}

	// And the switch really was left alone.
	for _, vlan := range device.VLANs() {
		if vlan.VID == 1 && len(vlan.Untagged) != 2 {
			t.Errorf("the management VLAN was modified: untagged = %v", vlan.Untagged)
		}
	}
}

func TestWritingPVIDLeavesUnmanagedPortsAlone(t *testing.T) {
	// The firmware wants the whole table on every write, so a driver that
	// sent only the port it was asked about would blank the rest.
	device := fakeswitch.New("s3cret")
	client := clientForFake(t, device, "s3cret")

	before := device.PVIDs()
	if _, err := client.WritePVID(context.Background(), map[int]int{5: 1003}, false); err != nil {
		t.Fatalf("WritePVID: %v", err)
	}

	after := device.PVIDs()
	if after[5] != 1003 {
		t.Errorf("port 5 PVID = %d, want 1003", after[5])
	}
	for port := 1; port <= 4; port++ {
		if after[port] != before[port] {
			t.Errorf("port %d PVID changed from %d to %d without being asked",
				port, before[port], after[port])
		}
	}
}
