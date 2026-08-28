package zyxel

import (
	"context"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

// Switch-wide settings that live on zManagement.html and zIGMP_Snooping.html,
// plus the per-port rate limits on zadvenced_set_modify.html.
//
// Every value here is read from the page the firmware itself renders, so the
// current configuration is whatever the device says it is rather than whatever
// this provider last wrote.
var (
	eeeBitRE        = regexp.MustCompile(`eeeinfo_ds\s*=\s*\{[^}]*enable_bit\s*:\s*(0x[0-9a-fA-F]+|\d+)`)
	ledStateRE      = regexp.MustCompile(`var\s+led_eco\s*=\s*(\d+)`)
	mgmtVLANRE      = regexp.MustCompile(`mgmt_vlan\s*:\s*\[\s*'?(\d+)'?\s*\]`)
	snmpV2RE        = regexp.MustCompile(`snmp_info\s*=\s*\{[^}]*snmpv2\s*:\s*(\d+)`)
	igmpStateRE     = regexp.MustCompile(`igmp_ds\s*=\s*\{[^}]*?\bstate\s*:\s*(\d+)`)
	igmpDropRE      = regexp.MustCompile(`igmp_ds\s*=\s*\{[^}]*umcdropState\s*:\s*(\d+)`)
	igmpRouterRE    = regexp.MustCompile(`igmp_ds\s*=\s*\{[^}]*\brstic\s*:\s*(\d+)`)
	ingressRateRE   = regexp.MustCompile(`portspeed_info\s*=\s*\{\s*ingress\s*:\s*\[([0-9,\s]*)\]`)
	egressRateRE    = regexp.MustCompile(`egress\s*:\s*\[([0-9,\s]*)\]`)
	portIsolationRE = regexp.MustCompile(`var\s+port_iso\s*=\s*(\d+)`)
)

// rateUnitKbps is the step the firmware stores rates in: the page divides the
// figure a person types by 32 before sending it, and multiplies on the way
// back.
const rateUnitKbps = 32

// Management is the switch-wide configuration this driver can reach.
type Management struct {
	// EEE is 802.3az energy-efficient Ethernet, applied to every port at once
	// on this hardware.
	EEE bool
	// LED is the firmware's own Disable/Enable control for the panel lights.
	LED bool
	// ManagementVLAN is read-only here: changing it is how a switch is lost.
	ManagementVLAN int
	SNMPEnabled    bool

	IGMPSnooping bool
	// IGMPUnknownDrop discards multicast nobody asked for instead of flooding
	// it to every port.
	IGMPUnknownDrop bool
	// IGMPStaticRouterPort is 0 for automatic discovery, else a port number.
	IGMPStaticRouterPort int

	// PortIsolationUplink is 0 when ports may talk to each other, otherwise
	// the one port every other port is allowed to reach.
	PortIsolationUplink int

	// IngressKbps and EgressKbps are per-port caps, 0 meaning no limit. Index
	// 0 is port 1.
	IngressKbps []int
	EgressKbps  []int
}

func firstInt(re *regexp.Regexp, text string) (int, bool) {
	match := re.FindStringSubmatch(text)
	if match == nil {
		return 0, false
	}
	raw := match[1]
	if strings.HasPrefix(raw, "0x") || strings.HasPrefix(raw, "0X") {
		value, err := strconv.ParseInt(raw[2:], 16, 64)
		return int(value), err == nil
	}
	value, err := strconv.Atoi(raw)
	return value, err == nil
}

func parseManagementPage(html string, into *Management) error {
	bit, ok := firstInt(eeeBitRE, html)
	if !ok {
		return fmt.Errorf("the management page carried no eeeinfo_ds block; the " +
			"firmware may differ from the V1.00(ACPS.2)C0 this driver was written against")
	}
	// The firmware turns EEE on for every port at once; a non-zero bitmap is
	// therefore "on".
	into.EEE = bit != 0
	if led, ok := firstInt(ledStateRE, html); ok {
		into.LED = led != 0
	}
	if vid, ok := firstInt(mgmtVLANRE, html); ok {
		into.ManagementVLAN = vid
	}
	if snmp, ok := firstInt(snmpV2RE, html); ok {
		into.SNMPEnabled = snmp != 0
	}
	return nil
}

func parseIGMPPage(html string, into *Management) error {
	state, ok := firstInt(igmpStateRE, html)
	if !ok {
		return fmt.Errorf("the IGMP page carried no igmp_ds block")
	}
	into.IGMPSnooping = state != 0
	if drop, ok := firstInt(igmpDropRE, html); ok {
		into.IGMPUnknownDrop = drop != 0
	}
	// rstic is a port bitmap with bit 0 for port 1; zero means automatic.
	if bitmap, ok := firstInt(igmpRouterRE, html); ok && bitmap != 0 {
		for i := 0; i < 32; i++ {
			if bitmap&(1<<uint(i)) != 0 {
				into.IGMPStaticRouterPort = i + 1
				break
			}
		}
	}
	return nil
}

func parseAdvancedPage(html string, into *Management) {
	scale := func(match []string) []int {
		if match == nil {
			return nil
		}
		values := []int{}
		for _, raw := range strings.Split(match[1], ",") {
			raw = strings.TrimSpace(raw)
			if raw == "" {
				continue
			}
			if value, err := strconv.Atoi(raw); err == nil {
				values = append(values, value*rateUnitKbps)
			}
		}
		return values
	}
	into.IngressKbps = scale(ingressRateRE.FindStringSubmatch(html))
	into.EgressKbps = scale(egressRateRE.FindStringSubmatch(html))

	// 2046 is every port, which is the firmware's way of saying "no isolation".
	if bitmap, ok := firstInt(portIsolationRE, html); ok && bitmap != 0 && bitmap != 2046 {
		for i := 0; i < 32; i++ {
			if bitmap&(1<<uint(i)) != 0 {
				into.PortIsolationUplink = i
				break
			}
		}
	}
}

// readManagement gathers the three pages. The session must be held.
func (c *Client) readManagement(ctx context.Context) (Management, error) {
	var management Management

	html, err := c.get(ctx, "zManagement.html")
	if err != nil {
		return management, err
	}
	if err := parseManagementPage(html, &management); err != nil {
		return management, err
	}

	html, err = c.get(ctx, "zIGMP_Snooping.html")
	if err != nil {
		return management, err
	}
	if err := parseIGMPPage(html, &management); err != nil {
		return management, err
	}

	html, err = c.get(ctx, "zadvenced_set_modify.html")
	if err != nil {
		return management, err
	}
	parseAdvancedPage(html, &management)
	return management, nil
}

// ReadManagement reads every switch-wide setting this driver knows.
func (c *Client) ReadManagement(ctx context.Context) (Management, error) {
	if cached, ok := c.cachedManagement(); ok {
		return cached, nil
	}
	if c.Password == "" {
		return Management{}, fmt.Errorf("%w: these settings live behind the login", ErrAuth)
	}

	var management Management
	err := c.withSession(ctx, func(ctx context.Context) error {
		var err error
		management, err = c.readManagement(ctx)
		return err
	})
	if err == nil {
		c.cacheManagement(management)
	}
	return management, err
}

// WriteManagement applies the switch-wide settings, sending only what changed.
//
// Each knob has its own endpoint on this firmware, so this walks them one at a
// time inside a single session rather than opening one per field.
func (c *Client) WriteManagement(ctx context.Context, want Management, force bool) (Management, error) {
	c.forget()

	if want.IGMPStaticRouterPort < 0 {
		return Management{}, fmt.Errorf("the IGMP static router port cannot be negative")
	}

	var result Management
	err := c.withSession(ctx, func(ctx context.Context) error {
		current, err := c.readManagement(ctx)
		if err != nil {
			return err
		}

		if want.SNMPEnabled != current.SNMPEnabled && !want.SNMPEnabled && !force {
			return fmt.Errorf("%w: switching SNMP off blinds anything polling this "+
				"switch — a monitoring system may be reading its port counters that way", ErrUnsafe)
		}

		// EEE. The firmware applies it to every port at once and takes about
		// ten seconds to settle, which is why the page reloads itself after.
		if want.EEE != current.EEE {
			ports := 0
			for i := 0; i < 32; i++ {
				ports |= 1 << uint(i)
			}
			if _, err := c.get(ctx, fmt.Sprintf("zeeeSet.cgi?state=%s&portBit=%d",
				boolField(want.EEE), ports)); err != nil {
				return err
			}
		}

		if want.LED != current.LED {
			form := url.Values{"led_state_f": {boolField(want.LED)}}
			if _, err := c.fetch(ctx, "POST", "led_cfg.cgi", form.Encode(),
				"application/x-www-form-urlencoded"); err != nil {
				return err
			}
		}

		if want.SNMPEnabled != current.SNMPEnabled {
			state := boolField(want.SNMPEnabled)
			if _, err := c.get(ctx, fmt.Sprintf("zsnmp.cgi?snmpv1=%s&snmpv2=%s",
				state, state)); err != nil {
				return err
			}
		}

		// IGMP. Only the fields that move are sent, which is what the page
		// itself does.
		params := []string{}
		if want.IGMPSnooping != current.IGMPSnooping {
			params = append(params, "igmp_mode="+boolField(want.IGMPSnooping))
		}
		if want.IGMPSnooping && want.IGMPStaticRouterPort != current.IGMPStaticRouterPort {
			bitmap := 0
			if want.IGMPStaticRouterPort > 0 {
				bitmap = 1 << uint(want.IGMPStaticRouterPort-1)
			}
			params = append(params, "mrouter="+strconv.Itoa(bitmap))
		}
		if want.IGMPUnknownDrop != current.IGMPUnknownDrop {
			params = append(params, "MCstate="+boolField(want.IGMPUnknownDrop))
		}
		if len(params) > 0 {
			params = append(params, "trunkBitMap=0")
			if _, err := c.get(ctx, "zigmpSnooping.cgi?"+strings.Join(params, "&")); err != nil {
				return err
			}
		}

		if want.PortIsolationUplink != current.PortIsolationUplink {
			// 2046 is every port, the firmware's way of saying "no isolation".
			bitmap := 2046
			if want.PortIsolationUplink > 0 {
				bitmap = 1 << uint(want.PortIsolationUplink)
			}
			if _, err := c.get(ctx, fmt.Sprintf("zmvlanSet.cgi?portbmp=%d&trunkBitMap=0",
				bitmap)); err != nil {
				return err
			}
		}

		after, err := c.readManagement(ctx)
		if err != nil {
			return err
		}
		if after.EEE != want.EEE || after.LED != want.LED ||
			after.SNMPEnabled != want.SNMPEnabled ||
			after.IGMPSnooping != want.IGMPSnooping ||
			after.IGMPUnknownDrop != want.IGMPUnknownDrop {
			return fmt.Errorf("the switch did not apply every setting: asked for "+
				"eee=%t led=%t snmp=%t igmp=%t igmp_drop=%t, it reports "+
				"%t %t %t %t %t",
				want.EEE, want.LED, want.SNMPEnabled, want.IGMPSnooping, want.IGMPUnknownDrop,
				after.EEE, after.LED, after.SNMPEnabled, after.IGMPSnooping, after.IGMPUnknownDrop)
		}
		result = after
		return nil
	})
	return result, err
}

// WritePortRates caps one port's ingress and egress, in kbps. Zero lifts the
// cap.
//
// The firmware wants every port's figure on each submit and divides them by 32
// on the way out, so the others are read and sent back unchanged; g_selMask
// names the one port meant to move.
func (c *Client) WritePortRates(ctx context.Context, port, ingressKbps, egressKbps int) (Management, error) {
	c.forget()

	for _, rate := range []int{ingressKbps, egressKbps} {
		if rate != 0 && (rate < 32 || rate > 1000000) {
			return Management{}, fmt.Errorf("a rate must be 0 for no limit, or between "+
				"32 and 1000000 kbps; got %d", rate)
		}
		if rate%rateUnitKbps != 0 {
			return Management{}, fmt.Errorf("the firmware stores rates in steps of %d kbps, "+
				"so %d cannot be represented exactly", rateUnitKbps, rate)
		}
	}

	var result Management
	err := c.withSession(ctx, func(ctx context.Context) error {
		current, err := c.readManagement(ctx)
		if err != nil {
			return err
		}
		if port < 1 || port > len(current.IngressKbps) {
			return fmt.Errorf("port %d does not exist on this switch, which has %d",
				port, len(current.IngressKbps))
		}

		// The page emits eight slots whatever the model.
		ingress, egress := make([]string, 8), make([]string, 8)
		for i := 0; i < 8; i++ {
			in, out := 0, 0
			if i < len(current.IngressKbps) {
				in, out = current.IngressKbps[i], current.EgressKbps[i]
			}
			if i == port-1 {
				in, out = ingressKbps, egressKbps
			}
			ingress[i] = strconv.Itoa(in / rateUnitKbps)
			egress[i] = strconv.Itoa(out / rateUnitKbps)
		}

		form := url.Values{
			// Underscore-separated, with a trailing separator, as the page builds it.
			"g_ingressSpeed": {strings.Join(ingress, "_") + "_"},
			"g_egressSpeed":  {strings.Join(egress, "_") + "_"},
			"g_selMask":      {strconv.Itoa(1 << uint(port-1))},
		}
		if _, err := c.fetch(ctx, "POST", "zrate_limit_set.cgi", form.Encode(),
			"application/x-www-form-urlencoded"); err != nil {
			return err
		}

		after, err := c.readManagement(ctx)
		if err != nil {
			return err
		}
		if after.IngressKbps[port-1] != ingressKbps || after.EgressKbps[port-1] != egressKbps {
			return fmt.Errorf("port %d rates did not apply: asked for ingress=%d egress=%d, "+
				"switch reports %d and %d", port, ingressKbps, egressKbps,
				after.IngressKbps[port-1], after.EgressKbps[port-1])
		}
		result = after
		return nil
	})
	return result, err
}
