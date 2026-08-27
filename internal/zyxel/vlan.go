package zyxel

import (
	"context"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
)

// kv is one query parameter. The CGI endpoints are given their parameters in
// the order the firmware's own JavaScript emits them: url.Values.Encode()
// sorts keys alphabetically, and there is no evidence this firmware tolerates
// that, so order is preserved deliberately rather than left to the encoder.
type kv struct {
	key   string
	value string
}

// n builds a numeric parameter; s builds a textual one. Only `action` is
// textual, but it is the first parameter of every write.
func n(key string, value int) kv { return kv{key, strconv.Itoa(value)} }
func s(key, value string) kv     { return kv{key, value} }

func query(pairs []kv) string {
	parts := make([]string, 0, len(pairs))
	for _, pair := range pairs {
		parts = append(parts, url.QueryEscape(pair.key)+"="+url.QueryEscape(pair.value))
	}
	return strings.Join(parts, "&")
}

// -- reads -----------------------------------------------------------------

// ReadVLANTable reads VLAN membership. Needs no session: the endpoint is
// unauthenticated, which is a firmware weakness rather than a design choice,
// but it means VLAN state can be read without touching the exclusive session.
func (c *Client) ReadVLANTable(ctx context.Context) ([]VLANEntry, error) {
	payload, err := c.get(ctx, "vlanEntry.xml")
	if err != nil {
		return nil, err
	}
	return ParseVLANEntries(payload), nil
}

// parseListPage pulls PVIDs, port count, 802.1Q state and the management VLAN
// out of zVLAN_1Q_List.html.
func parseListPage(html string) (pvid map[int]int, portCount int, enabled bool, management int) {
	pvid = map[int]int{}
	if match := pvidsRE.FindStringSubmatch(html); match != nil {
		for index, raw := range strings.Split(match[1], ",") {
			raw = strings.TrimSpace(raw)
			if raw == "" {
				continue
			}
			if value, err := strconv.Atoi(raw); err == nil {
				pvid[index+1] = value
			}
		}
	}

	portCount = len(pvid)
	if match := portMaxRE.FindStringSubmatch(html); match != nil {
		if value, err := strconv.Atoi(match[1]); err == nil {
			portCount = value
		}
	}

	// 0 disables 802.1Q; anything else is one of the enabled modes.
	enabled = true
	if match := vlanStateRE.FindStringSubmatch(html); match != nil {
		enabled = match[1] != "0"
	}

	if match := mgmtVlanRE.FindStringSubmatch(html); match != nil {
		if value, err := strconv.Atoi(match[1]); err == nil {
			management = value
		}
	}
	return pvid, portCount, enabled, management
}

// readState reads the full state, assuming a session is already held.
func (c *Client) readState(ctx context.Context) (Config, error) {
	entries, err := c.ReadVLANTable(ctx)
	if err != nil {
		return Config{}, err
	}
	html, err := c.get(ctx, "zVLAN_1Q_List.html")
	if err != nil {
		return Config{}, err
	}
	pvid, portCount, enabled, management := parseListPage(html)
	return Config{
		VLANs:          entries,
		PVID:           pvid,
		PortCount:      portCount,
		Enabled:        enabled,
		ManagementVLAN: management,
	}, nil
}

// ReadConfig reads the full VLAN state.
//
// Membership comes from the unauthenticated endpoint. PVIDs live on a page
// that does require a session, so when no password is configured the result
// is returned with Partial set rather than failing: knowing the VLANs without
// the PVIDs is still worth having, and it costs the switch nothing.
//
// The authenticated answer is cached, because every call to it claims the
// device's single web session and locks its owner out of the web UI for the
// duration. A refresh over a five-port switch asks once per port, and ten
// login/logout cycles on a slow CPU is both slow and rude. The cache is
// dropped by any write and expires on its own; a provider process lives only
// for one CLI command, so nothing else can be changing the switch underneath
// it that was not already a race.
func (c *Client) ReadConfig(ctx context.Context) (Config, error) {
	if cached, ok := c.cachedConfig(); ok {
		return cached, nil
	}

	entries, err := c.ReadVLANTable(ctx)
	if err != nil {
		return Config{}, err
	}

	if c.Password == "" {
		highest := 0
		for _, entry := range entries {
			for _, port := range append(append([]int{}, entry.Tagged...), entry.Untagged...) {
				if port > highest {
					highest = port
				}
			}
		}
		return Config{VLANs: entries, PortCount: highest, Partial: true}, nil
	}

	var config Config
	err = c.withSession(ctx, func(ctx context.Context) error {
		config, err = c.readState(ctx)
		return err
	})
	if err == nil {
		c.cacheConfig(config)
	}
	return config, err
}

// -- safety ----------------------------------------------------------------

// guard refuses changes that would very likely cut the switch off.
//
// The switch reaches the network through whichever port carries its management
// VLAN. Take that away and the web interface becomes unreachable — recovery
// means physically resetting the switch, which is not something to discover
// after an apply has already run.
func guard(current Config, proposed []VLANEntry, proposedPVID map[int]int, force bool) error {
	if force {
		return nil
	}
	management := current.ManagementVLAN
	if management == 0 {
		// Without a session the management VLAN is unknown; do not invent one.
		return nil
	}

	members := func(entries []VLANEntry, vid int) map[int]bool {
		for _, entry := range entries {
			if entry.VID == vid {
				return entry.Members()
			}
		}
		return map[int]bool{}
	}

	before := members(current.VLANs, management)
	after := members(proposed, management)

	lost := []int{}
	for port := range before {
		if !after[port] {
			lost = append(lost, port)
		}
	}
	if len(lost) > 0 {
		sort.Ints(lost)
		return fmt.Errorf("%w: this would remove port(s) %v from the management "+
			"VLAN %d; the switch could become unreachable", ErrUnsafe, lost, management)
	}
	if len(before) > 0 && len(after) == 0 {
		return fmt.Errorf("%w: this would delete the management VLAN %d",
			ErrUnsafe, management)
	}

	// A port whose PVID points at a VLAN that will not exist ends up with
	// untagged traffic going nowhere.
	known := make(map[int]bool, len(proposed))
	for _, entry := range proposed {
		known[entry.VID] = true
	}
	ports := make([]int, 0, len(proposedPVID))
	for port := range proposedPVID {
		ports = append(ports, port)
	}
	sort.Ints(ports)
	for _, port := range ports {
		if vid := proposedPVID[port]; !known[vid] {
			return fmt.Errorf("%w: port %d has PVID %d, which is not among the "+
				"VLANs that would remain", ErrUnsafe, port, vid)
		}
	}
	return nil
}

// -- writes ----------------------------------------------------------------

// WriteVLAN creates or modifies one VLAN, then reads it back to confirm.
func (c *Client) WriteVLAN(ctx context.Context, entry VLANEntry, force bool) (Config, error) {
	c.forget()
	if err := validateVID(entry.VID); err != nil {
		return Config{}, err
	}

	tagged := sorted(entry.Tagged)
	untagged := disjoint(tagged, entry.Untagged)

	var result Config
	err := c.withSession(ctx, func(ctx context.Context) error {
		current, err := c.readState(ctx)
		if err != nil {
			return err
		}
		existing, exists := current.VLAN(entry.VID)

		wanted := VLANEntry{VID: entry.VID, Name: entry.Name, Tagged: tagged, Untagged: untagged}
		proposed := append(current.withoutVLAN(entry.VID), wanted)
		if err := guard(current, proposed, current.PVID, force); err != nil {
			return err
		}

		taggedMap := EncodePorts(tagged)
		untaggedMap := EncodePorts(untagged)

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
				n("vid", entry.VID),
				n("vidx", existing.Index),
				n("untagMbrs", untaggedMap),
				n("tagMbrs", taggedMap),
				n("addPbmp", EncodePorts(added)),
				n("delPbmp", EncodePorts(removed)),
				n("changePcnt", len(added)+len(removed)),
				// The firmware names a VLAN by its own id; the web UI shows no
				// free-text field for it.
				n("name", entry.VID),
				n("trunkBitMap", 0),
			}
		} else {
			params = []kv{
				s("action", "add"),
				n("vid", entry.VID),
				n("vidx", firstFreeIndex(current.VLANs)),
				n("fid", 0),
				n("untagMbrs", untaggedMap),
				n("tagMbrs", taggedMap),
				n("name", entry.VID),
				n("trunkBitMap", 0),
			}
		}

		if _, err := c.get(ctx, "zqvlanSet.cgi?"+query(params)); err != nil {
			return err
		}
		result, err = c.verify(ctx, entry.VID, &wanted)
		return err
	})
	return result, err
}

// DeleteVLAN removes one VLAN, then reads it back to confirm.
func (c *Client) DeleteVLAN(ctx context.Context, vid int, force bool) (Config, error) {
	c.forget()
	var result Config
	err := c.withSession(ctx, func(ctx context.Context) error {
		current, err := c.readState(ctx)
		if err != nil {
			return err
		}
		existing, exists := current.VLAN(vid)
		if !exists {
			return fmt.Errorf("VLAN %d does not exist on this switch", vid)
		}

		// The firmware refuses this too, but failing here gives a clearer
		// message than the silent no-op the CGI would return.
		assigned := []int{}
		for port, pv := range current.PVID {
			if pv == vid {
				assigned = append(assigned, port)
			}
		}
		if len(assigned) > 0 {
			sort.Ints(assigned)
			return fmt.Errorf("%w: VLAN %d is the PVID of port(s) %v; move those "+
				"ports to another VLAN first", ErrInUseAsPVID, vid, assigned)
		}

		if err := guard(current, current.withoutVLAN(vid), current.PVID, force); err != nil {
			return err
		}

		params := []kv{
			s("action", "del"),
			n("vid", vid),
			n("vidx", existing.Index),
			n("untagMbrs", EncodePorts(existing.Untagged)),
			n("tagMbrs", EncodePorts(existing.Tagged)),
			n("trunkBitMap", 0),
		}
		if _, err := c.get(ctx, "zqvlanSet.cgi?"+query(params)); err != nil {
			return err
		}
		result, err = c.verify(ctx, vid, nil)
		return err
	})
	return result, err
}

// WritePVID sets the native VLAN of one or more ports. Unlisted ports keep
// theirs: the firmware wants the whole table on every write, so the current
// values are read and merged rather than assumed.
func (c *Client) WritePVID(ctx context.Context, wanted map[int]int, force bool) (Config, error) {
	c.forget()
	var result Config
	err := c.withSession(ctx, func(ctx context.Context) error {
		current, err := c.readState(ctx)
		if err != nil {
			return err
		}
		if len(current.PVID) == 0 {
			return fmt.Errorf("cannot read the current PVIDs on %s, so they "+
				"cannot be changed safely", c.Host)
		}

		merged := make(map[int]int, len(current.PVID))
		for port, vid := range current.PVID {
			merged[port] = vid
		}
		for port, vid := range wanted {
			merged[port] = vid
		}

		if err := guard(current, current.VLANs, merged, force); err != nil {
			return err
		}

		changed := changedPorts(current.PVID, merged)
		if len(changed) == 0 {
			result = current
			return nil
		}

		ports := make([]int, 0, len(merged))
		for port := range merged {
			ports = append(ports, port)
		}
		sort.Ints(ports)

		params := make([]kv, 0, len(ports)+3)
		for _, port := range ports {
			// The firmware indexes these from zero.
			params = append(params, n(fmt.Sprintf("vid%d", port-1), merged[port]))
		}
		params = append(params,
			n("changePbmp", EncodePorts(changed)),
			n("changePcnt", len(changed)),
			n("trunkBitMap", 0),
		)

		if _, err := c.get(ctx, "zqvlanPvidSet.cgi?"+query(params)); err != nil {
			return err
		}

		after, err := c.readState(ctx)
		if err != nil {
			return err
		}
		mismatched := []string{}
		for _, port := range changed {
			if after.PVID[port] != merged[port] {
				mismatched = append(mismatched, fmt.Sprintf(
					"port %d: asked %d, switch reports %d",
					port, merged[port], after.PVID[port]))
			}
		}
		if len(mismatched) > 0 {
			return fmt.Errorf("the switch did not apply every PVID change: %s",
				strings.Join(mismatched, "; "))
		}
		result = after
		return nil
	})
	return result, err
}

// verify reads the configuration back and checks the device did what was asked.
//
// These CGI endpoints answer 200 with a redirect page whether or not they
// accepted the request, so the reply proves nothing on its own. Called with
// the session held.
func (c *Client) verify(ctx context.Context, vid int, expected *VLANEntry) (Config, error) {
	after, err := c.readState(ctx)
	if err != nil {
		return Config{}, err
	}
	found, exists := after.VLAN(vid)

	if expected == nil {
		if exists {
			return Config{}, fmt.Errorf("VLAN %d is still present after the delete", vid)
		}
		return after, nil
	}

	if !exists {
		return Config{}, fmt.Errorf("VLAN %d is absent after the write", vid)
	}

	wantTagged := sorted(expected.Tagged)
	wantUntagged := disjoint(wantTagged, expected.Untagged)
	if !equalPorts(found.Tagged, wantTagged) || !equalPorts(found.Untagged, wantUntagged) {
		return Config{}, fmt.Errorf("VLAN %d did not apply as requested: asked for "+
			"tagged=%v untagged=%v, switch reports tagged=%v untagged=%v",
			vid, wantTagged, wantUntagged, sorted(found.Tagged), sorted(found.Untagged))
	}
	return after, nil
}

func equalPorts(a, b []int) bool {
	a, b = sorted(a), sorted(b)
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
