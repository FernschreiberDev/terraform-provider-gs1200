package zyxel

import (
	"sort"
	"strconv"
	"strings"
)

// DecodePorts turns a device bitmap into 1-based port numbers.
//
// The device places port 1 at bit 1, so the map is halved first. A GS1200-5
// reporting 6 means ports 1 and 2.
func DecodePorts(bitmap int) []int {
	shifted := bitmap >> 1
	ports := []int{}
	for i := 0; i < 32; i++ {
		if shifted&(1<<uint(i)) != 0 {
			ports = append(ports, i+1)
		}
	}
	return ports
}

// EncodePorts turns 1-based port numbers into a device bitmap. Inverse of
// DecodePorts: zqvlan_modify.html computes tagBmp = tpbmp << 1 where tpbmp
// has port 1 at bit 0, so port 1 must encode to 2.
func EncodePorts(ports []int) int {
	value := 0
	for _, port := range ports {
		if port >= 1 {
			value |= 1 << uint(port)
		}
	}
	return value
}

// ParseVLANEntries parses /vlanEntry.xml.
//
// Despite the name it is not XML but idx,vid,name,mbrs,tagMbrs,untagMbrs
// records separated by semicolons, with a trailing separator. Records that do
// not parse are skipped rather than failing the whole read: a firmware that
// grows a field should not take the switch out of management.
func ParseVLANEntries(payload string) []VLANEntry {
	entries := []VLANEntry{}
	for _, record := range strings.Split(payload, ";") {
		record = strings.TrimSpace(record)
		if record == "" {
			continue
		}
		fields := strings.Split(record, ",")
		if len(fields) < 6 {
			continue
		}
		index, err := strconv.Atoi(strings.TrimSpace(fields[0]))
		if err != nil {
			continue
		}
		vid, err := strconv.Atoi(strings.TrimSpace(fields[1]))
		if err != nil {
			continue
		}
		taggedMap, err := strconv.Atoi(strings.TrimSpace(fields[4]))
		if err != nil {
			continue
		}
		untaggedMap, err := strconv.Atoi(strings.TrimSpace(fields[5]))
		if err != nil {
			continue
		}

		tagged := DecodePorts(taggedMap)
		entries = append(entries, VLANEntry{
			VID:  vid,
			Name: strings.TrimSpace(fields[2]),
			// The firmware keeps them disjoint, but a port appearing in both
			// would otherwise be reported twice.
			Tagged:   tagged,
			Untagged: disjoint(tagged, DecodePorts(untaggedMap)),
			Index:    index,
		})
	}
	return entries
}

// portsFromMap flattens a port->value map into the sorted ports whose value
// changed, which is what the firmware's changePbmp expects.
func changedPorts(before, after map[int]int) []int {
	changed := []int{}
	for port, vid := range after {
		if before[port] != vid {
			changed = append(changed, port)
		}
	}
	sort.Ints(changed)
	return changed
}
