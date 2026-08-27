// Package zyxel talks to a Zyxel GS1200 (v3) over its web interface.
//
// The GS1200 exposes almost nothing over SNMP beyond MIB-II counters — no
// Q-BRIDGE-MIB, so VLANs are invisible there. Everything in this package was
// derived by reading the firmware's own JavaScript on a GS1200-5v3 running
// V1.00(ACPS.2)C0, and is a port of the Python driver in the switchboard
// project, which was itself written against that hardware.
//
// Protocol, as observed:
//
//   - Login — POST logon.cgi with password=<sha256 hex of the password>. The
//     hashing is done by the page before submitting, so the plaintext never
//     leaves the browser; this package does the same.
//   - One session at a time. While a session is held nobody else can reach
//     the web UI, so this package logs in only when it must and always logs
//     out afterwards.
//   - Reading needs no session. /vlanEntry.xml serves the full VLAN table to
//     anyone who asks, while the HTML pages correctly redirect to the login.
//   - Port bitmaps are shifted by one. The device reports and accepts maps
//     where port 1 is bit 1, not bit 0. Getting this wrong silently moves
//     every port by one position — on a write, that reassigns live traffic to
//     the wrong physical socket.
//   - Tagged and untagged are disjoint in a write: untagMbrs carries the
//     members that are not in tagMbrs.
package zyxel

import (
	"errors"
	"fmt"
	"sort"
)

// Sentinel errors, so callers can tell a wrong password from a busy switch
// from a change that was understood and refused. Wrapped with %w throughout.
var (
	// ErrAuth means the credentials were rejected.
	ErrAuth = errors.New("authentication failed")
	// ErrBusy means the device's single web session is held by someone else.
	ErrBusy = errors.New("switch busy")
	// ErrUnsafe means the change was refused because it would likely cut the
	// switch off. Separate from a generic failure so the caller can offer an
	// explicit override rather than presenting a dead end.
	ErrUnsafe = errors.New("unsafe change refused")
	// ErrInUseAsPVID means a VLAN cannot be deleted because a port still has
	// it as its native VLAN. Distinct because the way out is specific: move
	// those ports first, and `force` does not help.
	ErrInUseAsPVID = errors.New("VLAN still in use as a PVID")
)

// VLANEntry is one 802.1Q VLAN as the device reports it. Ports are 1-based.
type VLANEntry struct {
	VID  int
	Name string
	// Tagged and Untagged are kept sorted and disjoint.
	Tagged   []int
	Untagged []int
	// Index is the vendor row index. Needed to edit or delete, because the
	// device addresses VLANs by table slot rather than by VID. Zero means
	// "not known", which only happens for an entry we built rather than read.
	Index int
}

// Members is every port in the VLAN, tagged or not.
func (e VLANEntry) Members() map[int]bool {
	members := make(map[int]bool, len(e.Tagged)+len(e.Untagged))
	for _, p := range e.Tagged {
		members[p] = true
	}
	for _, p := range e.Untagged {
		members[p] = true
	}
	return members
}

// Config is the full VLAN state of a switch.
type Config struct {
	VLANs []VLANEntry
	// PVID maps a 1-based port number to its native VLAN id.
	PVID           map[int]int
	PortCount      int
	Enabled        bool
	ManagementVLAN int // 0 when unknown
	// Partial is true when the values came from the unauthenticated endpoint,
	// so PVIDs and anything else needing a session may be missing.
	Partial bool
}

// VLAN returns the entry for vid, and whether it exists.
func (c Config) VLAN(vid int) (VLANEntry, bool) {
	for _, entry := range c.VLANs {
		if entry.VID == vid {
			return entry, true
		}
	}
	return VLANEntry{}, false
}

// withoutVLAN is the VLAN list with vid removed — the basis of every
// "what would this look like afterwards" check.
func (c Config) withoutVLAN(vid int) []VLANEntry {
	proposed := make([]VLANEntry, 0, len(c.VLANs))
	for _, entry := range c.VLANs {
		if entry.VID != vid {
			proposed = append(proposed, entry)
		}
	}
	return proposed
}

// firstFreeIndex is the lowest table slot not already taken. The device
// addresses VLANs by slot, so creating one means choosing its slot.
func firstFreeIndex(vlans []VLANEntry) int {
	used := make(map[int]bool, len(vlans))
	for _, entry := range vlans {
		if entry.Index != 0 {
			used[entry.Index] = true
		}
	}
	index := 1
	for used[index] {
		index++
	}
	return index
}

// validateVID rejects what the device would reject, but with a clear message.
func validateVID(vid int) error {
	if vid < 1 || vid > 4094 {
		return fmt.Errorf("VLAN id %d is out of range (1-4094)", vid)
	}
	return nil
}

func sorted(ports []int) []int {
	out := append([]int(nil), ports...)
	sort.Ints(out)
	return out
}

// disjoint removes from untagged anything that is also tagged. The firmware
// keeps the two sets disjoint and expects writes to do the same.
func disjoint(tagged, untagged []int) []int {
	isTagged := make(map[int]bool, len(tagged))
	for _, p := range tagged {
		isTagged[p] = true
	}
	out := make([]int, 0, len(untagged))
	for _, p := range untagged {
		if !isTagged[p] {
			out = append(out, p)
		}
	}
	sort.Ints(out)
	return out
}
