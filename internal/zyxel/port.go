package zyxel

import (
	"context"
	"fmt"
	"sort"
)

// PortConfig is everything 802.1Q about one port, in the direction a person
// thinks about it.
//
// The device stores the opposite arrangement — a table of VLANs, each holding
// a bitmap of its member ports — so this type is a view, assembled by reading
// every VLAN row and asking "what does it say about this port".
type PortConfig struct {
	Port int
	// PVID is the VLAN an untagged frame arriving here is put into. Ingress.
	PVID int
	// Tagged and Untagged are VLAN ids, and describe egress: whether a frame
	// of that VLAN leaves this port carrying its 802.1Q tag, or stripped of it.
	Tagged   []int
	Untagged []int
}

// ReadPort assembles one port's view from a configuration already read.
func (c Config) ReadPort(port int) PortConfig {
	view := PortConfig{Port: port, PVID: c.PVID[port], Tagged: []int{}, Untagged: []int{}}
	for _, vlan := range c.VLANs {
		for _, p := range vlan.Tagged {
			if p == port {
				view.Tagged = append(view.Tagged, vlan.VID)
			}
		}
		for _, p := range vlan.Untagged {
			if p == port {
				view.Untagged = append(view.Untagged, vlan.VID)
			}
		}
	}
	sort.Ints(view.Tagged)
	sort.Ints(view.Untagged)
	return view
}

// applyVLANEntry sends one VLAN row to the device. It assumes the session is
// held and performs no checking of its own: the caller has already decided
// this change is safe and will read the result back.
func (c *Client) applyVLANEntry(ctx context.Context, current Config, wanted VLANEntry) error {
	tagged := sorted(wanted.Tagged)
	untagged := disjoint(tagged, wanted.Untagged)
	wanted.Tagged, wanted.Untagged = tagged, untagged

	existing, exists := current.VLAN(wanted.VID)

	var params []kv
	if exists {
		oldMembers := existing.Members()
		newMembers := wanted.Members()
		added, removed := []int{}, []int{}
		for port := range newMembers {
			if !oldMembers[port] {
				added = append(added, port)
			}
		}
		for port := range oldMembers {
			if !newMembers[port] {
				removed = append(removed, port)
			}
		}
		sort.Ints(added)
		sort.Ints(removed)
		params = []kv{
			s("action", "mod"),
			n("vid", wanted.VID),
			n("vidx", existing.Index),
			n("untagMbrs", EncodePorts(untagged)),
			n("tagMbrs", EncodePorts(tagged)),
			n("addPbmp", EncodePorts(added)),
			n("delPbmp", EncodePorts(removed)),
			n("changePcnt", len(added)+len(removed)),
			// The firmware names a VLAN by its own id; the web UI shows no
			// free-text field for it.
			n("name", wanted.VID),
			n("trunkBitMap", 0),
		}
	} else {
		params = []kv{
			s("action", "add"),
			n("vid", wanted.VID),
			n("vidx", firstFreeIndex(current.VLANs)),
			n("fid", 0),
			n("untagMbrs", EncodePorts(untagged)),
			n("tagMbrs", EncodePorts(tagged)),
			n("name", wanted.VID),
			n("trunkBitMap", 0),
		}
	}

	_, err := c.get(ctx, "zqvlanSet.cgi?"+query(params))
	return err
}

// EnsureVLAN creates a VLAN with no members if it does not exist yet.
//
// Membership belongs to the ports, so a VLAN created here is deliberately
// empty: it is the object that must exist before any port can point at it,
// nothing more. Creating one that already exists is a no-op rather than an
// error, because two ports joining the same new VLAN must not race.
func (c *Client) EnsureVLAN(ctx context.Context, vid int, force bool) (Config, error) {
	c.forget()
	if err := validateVID(vid); err != nil {
		return Config{}, err
	}

	var result Config
	err := c.withSession(ctx, func(ctx context.Context) error {
		current, err := c.readState(ctx)
		if err != nil {
			return err
		}
		if _, exists := current.VLAN(vid); exists {
			result = current
			return nil
		}

		wanted := VLANEntry{VID: vid}
		if err := guard(current, append(current.VLANs, wanted), current.PVID, force); err != nil {
			return err
		}
		if err := c.applyVLANEntry(ctx, current, wanted); err != nil {
			return err
		}
		result, err = c.verify(ctx, vid, &wanted)
		return err
	})
	return result, err
}

// WritePort applies one port's whole 802.1Q configuration.
//
// The device has no notion of a port's configuration, so this is a
// read-modify-write across every VLAN row the port appears in — and the one
// invariant that makes it composable is that **only this port's bit ever
// changes**. Two ports can therefore be written independently, in any order,
// without either undoing the other, which is what lets Terraform manage them
// as separate resources.
func (c *Client) WritePort(ctx context.Context, want PortConfig, force bool) (Config, error) {
	c.forget()

	wantTagged := sorted(want.Tagged)
	wantUntagged := disjoint(wantTagged, want.Untagged)

	var result Config
	err := c.withSession(ctx, func(ctx context.Context) error {
		current, err := c.readState(ctx)
		if err != nil {
			return err
		}
		if want.Port < 1 || (current.PortCount > 0 && want.Port > current.PortCount) {
			return fmt.Errorf("port %d does not exist on this switch, which has %d",
				want.Port, current.PortCount)
		}

		// Every VLAN named here has to exist already. Creating one implicitly
		// would mean a typo in a VLAN id silently provisions a new VLAN
		// instead of failing.
		for _, vid := range append(append([]int{}, wantTagged...), wantUntagged...) {
			if _, exists := current.VLAN(vid); !exists {
				return fmt.Errorf("VLAN %d does not exist on this switch; declare a "+
					"schaltwerk_zyxel_vlan for it before a port refers to it", vid)
			}
		}

		if err := checkPVIDIsUntagged(want.Port, want.PVID, wantUntagged, force); err != nil {
			return err
		}

		joins, leaves, proposed := planPortChanges(current, want.Port, wantTagged, wantUntagged)

		mergedPVID := map[int]int{}
		for port, vid := range current.PVID {
			mergedPVID[port] = vid
		}
		if want.PVID != 0 {
			mergedPVID[want.Port] = want.PVID
		}
		if err := guard(current, proposed, mergedPVID, force); err != nil {
			return err
		}

		// Order matters: join first, then move the PVID, then leave. At no
		// point is the port's native VLAN one it does not belong to — a state
		// the firmware refuses, and which would strand untagged traffic if it
		// did not.
		applied := current
		for _, entry := range joins {
			if err := c.applyVLANEntry(ctx, applied, entry); err != nil {
				return err
			}
			applied = replaceVLAN(applied, entry)
		}

		if want.PVID != 0 && current.PVID[want.Port] != want.PVID {
			if err := c.applyPVID(ctx, mergedPVID, []int{want.Port}); err != nil {
				return err
			}
		}

		for _, entry := range leaves {
			if err := c.applyVLANEntry(ctx, applied, entry); err != nil {
				return err
			}
			applied = replaceVLAN(applied, entry)
		}

		// The CGI endpoints answer 200 whether or not they applied anything,
		// so the only evidence is the configuration read back.
		after, err := c.readState(ctx)
		if err != nil {
			return err
		}
		got := after.ReadPort(want.Port)
		if !equalPorts(got.Tagged, wantTagged) || !equalPorts(got.Untagged, wantUntagged) ||
			(want.PVID != 0 && got.PVID != want.PVID) {
			return fmt.Errorf("port %d did not apply as requested: asked for "+
				"pvid=%d tagged=%v untagged=%v, switch reports pvid=%d tagged=%v untagged=%v",
				want.Port, want.PVID, wantTagged, wantUntagged,
				got.PVID, got.Tagged, got.Untagged)
		}
		result = after
		return nil
	})
	return result, err
}

// checkPVIDIsUntagged refuses the asymmetry that is almost always a mistake.
//
// A PVID naming a VLAN the port does not carry untagged means frames enter in
// that VLAN and leave tagged, or do not leave at all. The hardware permits it
// and reports nothing; the only sign is traffic that vanishes one way.
func checkPVIDIsUntagged(port, pvid int, untagged []int, force bool) error {
	if force || pvid == 0 {
		return nil
	}
	for _, vid := range untagged {
		if vid == pvid {
			return nil
		}
	}
	return fmt.Errorf("%w: port %d has pvid %d but does not carry VLAN %d untagged "+
		"(untagged = %v); untagged frames would arrive in a VLAN that cannot "+
		"carry them back out", ErrUnsafe, port, pvid, pvid, untagged)
}

// planPortChanges works out which VLAN rows have to change, splitting them
// into the ones this port joins and the ones it leaves.
func planPortChanges(current Config, port int, wantTagged, wantUntagged []int) (joins, leaves []VLANEntry, proposed []VLANEntry) {
	shouldTag := map[int]bool{}
	for _, vid := range wantTagged {
		shouldTag[vid] = true
	}
	shouldUntag := map[int]bool{}
	for _, vid := range wantUntagged {
		shouldUntag[vid] = true
	}

	for _, vlan := range current.VLANs {
		isTagged, isUntagged := false, false
		for _, p := range vlan.Tagged {
			if p == port {
				isTagged = true
			}
		}
		for _, p := range vlan.Untagged {
			if p == port {
				isUntagged = true
			}
		}

		wantT, wantU := shouldTag[vlan.VID], shouldUntag[vlan.VID]
		if isTagged == wantT && isUntagged == wantU {
			proposed = append(proposed, vlan)
			continue
		}

		// Only this port's bit moves; every other member is copied through.
		next := VLANEntry{VID: vlan.VID, Name: vlan.Name, Index: vlan.Index}
		for _, p := range vlan.Tagged {
			if p != port {
				next.Tagged = append(next.Tagged, p)
			}
		}
		for _, p := range vlan.Untagged {
			if p != port {
				next.Untagged = append(next.Untagged, p)
			}
		}
		if wantT {
			next.Tagged = append(next.Tagged, port)
		}
		if wantU {
			next.Untagged = append(next.Untagged, port)
		}
		next.Tagged = sorted(next.Tagged)
		next.Untagged = disjoint(next.Tagged, next.Untagged)

		if wantT || wantU {
			joins = append(joins, next)
		} else {
			leaves = append(leaves, next)
		}
		proposed = append(proposed, next)
	}
	return joins, leaves, proposed
}

func replaceVLAN(config Config, entry VLANEntry) Config {
	next := Config{
		PVID: config.PVID, PortCount: config.PortCount, Enabled: config.Enabled,
		ManagementVLAN: config.ManagementVLAN, Partial: config.Partial,
	}
	for _, vlan := range config.VLANs {
		if vlan.VID == entry.VID {
			next.VLANs = append(next.VLANs, entry)
		} else {
			next.VLANs = append(next.VLANs, vlan)
		}
	}
	return next
}
