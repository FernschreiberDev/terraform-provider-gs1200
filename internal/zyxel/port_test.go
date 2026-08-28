package zyxel

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// TestReadPortMatchesTheFleet turns the two switches' real VLAN tables inside
// out and checks the port-centric view against what a person reading the two units
// would say. The tagged/untagged sets come from the VLAN rows, the PVIDs from
// a different page entirely, so agreement across the two is meaningful.
func TestReadPortMatchesTheFleet(t *testing.T) {
	cases := []struct {
		switchName string
		xml        string
		pvids      map[int]int
		want       map[int]PortConfig
	}{
		{
			switchName: "gs1200",
			xml:        "1,1,1,0,0,6;2,8,8,0,2,32;3,1003,1003,0,6,24;",
			pvids:      map[int]int{1: 1, 2: 1, 3: 1003, 4: 1003, 5: 8},
			want: map[int]PortConfig{
				// Uplink: management untagged, the rest riding tagged.
				1: {Port: 1, PVID: 1, Untagged: []int{1}, Tagged: []int{8, 1003}},
				2: {Port: 2, PVID: 1, Untagged: []int{1}, Tagged: []int{1003}},
				3: {Port: 3, PVID: 1003, Untagged: []int{1003}, Tagged: []int{}},
				4: {Port: 4, PVID: 1003, Untagged: []int{1003}, Tagged: []int{}},
				5: {Port: 5, PVID: 8, Untagged: []int{8}, Tagged: []int{}},
			},
		},
		{
			switchName: "living",
			xml:        "1,1,1,0,0,6;2,1003,1003,0,6,56;3,1010,1010,0,8,0;",
			pvids:      map[int]int{1: 1, 2: 1, 3: 1003, 4: 1003, 5: 1003},
			want: map[int]PortConfig{
				1: {Port: 1, PVID: 1, Untagged: []int{1}, Tagged: []int{1003}},
				2: {Port: 2, PVID: 1, Untagged: []int{1}, Tagged: []int{1003}},
				// Hybrid: one VLAN native, another riding tagged.
				3: {Port: 3, PVID: 1003, Untagged: []int{1003}, Tagged: []int{1010}},
				4: {Port: 4, PVID: 1003, Untagged: []int{1003}, Tagged: []int{}},
				5: {Port: 5, PVID: 1003, Untagged: []int{1003}, Tagged: []int{}},
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.switchName, func(t *testing.T) {
			config := Config{VLANs: ParseVLANEntries(tc.xml), PVID: tc.pvids}
			for port, want := range tc.want {
				got := config.ReadPort(port)
				if got.PVID != want.PVID ||
					!equalPorts(got.Tagged, want.Tagged) ||
					!equalPorts(got.Untagged, want.Untagged) {
					t.Errorf("port %d\n got pvid=%d tagged=%v untagged=%v\nwant pvid=%d tagged=%v untagged=%v",
						port, got.PVID, got.Tagged, got.Untagged,
						want.PVID, want.Tagged, want.Untagged)
				}
			}
		})
	}
}

func TestWritePortMovesAPortBetweenVLANs(t *testing.T) {
	device := newSeededFake(t)
	client := clientForFake(t, device, "s3cret")

	// Port 5 leaves VLAN 8 for VLAN 1003: the everyday case, a device moved
	// from one network to another.
	if _, err := client.WritePort(context.Background(), PortConfig{
		Port: 5, PVID: 1003, Untagged: []int{1003},
	}, false); err != nil {
		t.Fatalf("WritePort: %v", err)
	}

	after, err := client.ReadConfig(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	got := after.ReadPort(5)
	if got.PVID != 1003 || !equalPorts(got.Untagged, []int{1003}) || len(got.Tagged) != 0 {
		t.Errorf("port 5 = pvid %d tagged %v untagged %v", got.PVID, got.Tagged, got.Untagged)
	}
	if vlan, _ := after.VLAN(8); len(vlan.Untagged) != 0 {
		t.Errorf("VLAN 8 still holds port 5: untagged %v", vlan.Untagged)
	}
}

// TestWritePortLeavesOtherPortsAlone is the invariant that makes one resource
// per port safe: a write may only move its own bit, or two ports would undo
// each other on every apply.
func TestWritePortLeavesOtherPortsAlone(t *testing.T) {
	device := newSeededFake(t)
	client := clientForFake(t, device, "s3cret")
	ctx := context.Background()

	before, err := client.ReadConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	untouched := map[int]PortConfig{}
	for port := 1; port <= 4; port++ {
		untouched[port] = before.ReadPort(port)
	}

	if _, err := client.WritePort(ctx, PortConfig{
		Port: 5, PVID: 1003, Untagged: []int{1003}, Tagged: []int{8},
	}, false); err != nil {
		t.Fatalf("WritePort: %v", err)
	}

	after, err := client.ReadConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for port, want := range untouched {
		got := after.ReadPort(port)
		if got.PVID != want.PVID ||
			!equalPorts(got.Tagged, want.Tagged) ||
			!equalPorts(got.Untagged, want.Untagged) {
			t.Errorf("port %d moved: before pvid=%d tagged=%v untagged=%v, after pvid=%d tagged=%v untagged=%v",
				port, want.PVID, want.Tagged, want.Untagged,
				got.PVID, got.Tagged, got.Untagged)
		}
	}
}

func TestWritePortRefusesAPVIDItCannotCarryBack(t *testing.T) {
	device := newSeededFake(t)
	client := clientForFake(t, device, "s3cret")

	// PVID 1003 while the port carries 1003 tagged only: frames arrive in a
	// VLAN that cannot carry them back out.
	_, err := client.WritePort(context.Background(), PortConfig{
		Port: 5, PVID: 1003, Tagged: []int{1003},
	}, false)
	if !errors.Is(err, ErrUnsafe) {
		t.Fatalf("want ErrUnsafe, got %v", err)
	}

	if _, err := client.WritePort(context.Background(), PortConfig{
		Port: 5, PVID: 1003, Tagged: []int{1003},
	}, true); err != nil {
		t.Fatalf("force must lift the refusal, got %v", err)
	}
}

func TestWritePortRefusesAnUnknownVLAN(t *testing.T) {
	device := newSeededFake(t)
	client := clientForFake(t, device, "s3cret")

	_, err := client.WritePort(context.Background(), PortConfig{
		Port: 3, PVID: 1003, Untagged: []int{1003}, Tagged: []int{4000},
	}, false)
	if err == nil || !strings.Contains(err.Error(), "VLAN 4000 does not exist") {
		t.Fatalf("a typo in a VLAN id must fail rather than provision one; got %v", err)
	}
}

func TestEnsureVLANIsIdempotent(t *testing.T) {
	device := newSeededFake(t)
	client := clientForFake(t, device, "s3cret")
	ctx := context.Background()

	// A fresh VLAN is born with no members: membership belongs to the ports.
	config, err := client.EnsureVLAN(ctx, 20, false)
	if err != nil {
		t.Fatalf("EnsureVLAN: %v", err)
	}
	entry, exists := config.VLAN(20)
	if !exists {
		t.Fatal("VLAN 20 was not created")
	}
	if len(entry.Tagged) != 0 || len(entry.Untagged) != 0 {
		t.Errorf("a fresh VLAN must be empty, got tagged=%v untagged=%v",
			entry.Tagged, entry.Untagged)
	}

	// Two ports joining the same new VLAN must not race each other.
	if _, err := client.EnsureVLAN(ctx, 20, false); err != nil {
		t.Fatalf("a second call must be a no-op, got %v", err)
	}
}
