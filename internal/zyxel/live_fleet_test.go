package zyxel

import (
	"reflect"
	"testing"
)

// These are the exact bytes both GS1200s served on 2026-08-27, paired with
// what the Python driver in the an SNMP poller project reported for the same
// switches at the same moment (its /api/config/<name> answer).
//
// The point is differential: two independently written implementations of the
// same undocumented format, checked against each other on live hardware. The
// bit-shift is where a port silently moves by one, and a single implementation
// can only ever prove itself self-consistent.
var liveUnits = []struct {
	name  string
	xml   string
	want  []VLANEntry
	pvids map[int]int // as the Python driver reported them, for the cross-check
}{
	{
		name: "gs1200 (192.0.2.10)",
		xml:  "1,1,1,0,0,6;2,8,8,0,2,32;3,1003,1003,0,6,24;",
		want: []VLANEntry{
			{VID: 1, Name: "1", Tagged: []int{}, Untagged: []int{1, 2}, Index: 1},
			{VID: 8, Name: "8", Tagged: []int{1}, Untagged: []int{5}, Index: 2},
			{VID: 1003, Name: "1003", Tagged: []int{1, 2}, Untagged: []int{3, 4}, Index: 3},
		},
		pvids: map[int]int{1: 1, 2: 1, 3: 1003, 4: 1003, 5: 8},
	},
	{
		name: "gs1200-2 / switch-b (192.0.2.11)",
		xml:  "1,1,1,0,0,6;2,1003,1003,0,6,56;3,1010,1010,0,8,0;",
		want: []VLANEntry{
			{VID: 1, Name: "1", Tagged: []int{}, Untagged: []int{1, 2}, Index: 1},
			{VID: 1003, Name: "1003", Tagged: []int{1, 2}, Untagged: []int{3, 4, 5}, Index: 2},
			{VID: 1010, Name: "1010", Tagged: []int{3}, Untagged: []int{}, Index: 3},
		},
		pvids: map[int]int{1: 1, 2: 1, 3: 1003, 4: 1003, 5: 1003},
	},
}

func TestMatchesThePythonDriverOnTheLiveFleet(t *testing.T) {
	for _, tc := range liveUnits {
		t.Run(tc.name, func(t *testing.T) {
			got := ParseVLANEntries(tc.xml)
			if len(got) != len(tc.want) {
				t.Fatalf("got %d entries, want %d", len(got), len(tc.want))
			}
			for i := range got {
				if got[i].VID != tc.want[i].VID ||
					got[i].Name != tc.want[i].Name ||
					got[i].Index != tc.want[i].Index ||
					!samePorts(got[i].Tagged, tc.want[i].Tagged) ||
					!samePorts(got[i].Untagged, tc.want[i].Untagged) {
					t.Errorf("entry %d\n got %+v\nwant %+v", i, got[i], tc.want[i])
				}
			}
		})
	}
}

func TestEveryPVIDNamesAVLANThePortIsUntaggedIn(t *testing.T) {
	// A port's native VLAN has to be one it carries untagged, or untagged
	// traffic arriving there goes nowhere. Both switches must satisfy this,
	// and the two facts come from different endpoints — so agreeing is
	// evidence the bitmaps were decoded onto the right ports.
	//
	// VLAN 1010 on the switch-b switch is tagged-only, which is exactly why the
	// check looks at untagged membership rather than membership.
	for _, tc := range liveUnits {
		t.Run(tc.name, func(t *testing.T) {
			untaggedIn := map[int]int{}
			for _, entry := range ParseVLANEntries(tc.xml) {
				for _, port := range entry.Untagged {
					untaggedIn[port] = entry.VID
				}
			}
			for port, pvid := range tc.pvids {
				if got := untaggedIn[port]; got != pvid {
					t.Errorf("port %d has PVID %d but is untagged in VLAN %d",
						port, pvid, got)
				}
			}
		})
	}
}

// TestARoundTripOfTheLiveTablesIsLossless proves the encoder and decoder agree
// on real data, not just on the handful of bitmaps written into the unit tests.
func TestARoundTripOfTheLiveTablesIsLossless(t *testing.T) {
	for _, tc := range liveUnits {
		for _, entry := range ParseVLANEntries(tc.xml) {
			tagged := EncodePorts(entry.Tagged)
			untagged := EncodePorts(entry.Untagged)
			if !reflect.DeepEqual(DecodePorts(tagged), normalise(entry.Tagged)) {
				t.Errorf("%s VLAN %d: tagged did not round trip", tc.name, entry.VID)
			}
			if !reflect.DeepEqual(DecodePorts(untagged), normalise(entry.Untagged)) {
				t.Errorf("%s VLAN %d: untagged did not round trip", tc.name, entry.VID)
			}
		}
	}
}

func samePorts(a, b []int) bool {
	if len(a) == 0 && len(b) == 0 {
		return true
	}
	return reflect.DeepEqual(a, b)
}

// normalise turns nil into the empty slice DecodePorts returns.
func normalise(ports []int) []int {
	if ports == nil {
		return []int{}
	}
	return ports
}
