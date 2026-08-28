package zyxel

import (
	"context"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Port-level settings, read from zPort.html and written to zport_setting.cgi.
//
// The page carries the whole current configuration in one JavaScript object:
//
//	all_info = {
//	  state:[1,1,1,1,1,],      // administratively up
//	  spd_cfg:[0,0,0,0,0,],    // configured speed, 0 = auto
//	  mode_cfg:[0,0,0,0,0,],
//	  fc_cfg:[0,0,0,0,0,],     // flow control
//	  ability:[31,31,31,31,31,],
//	  trunk_info:[0,0,0,0,0,],
//	}
//
// Reading it is what makes a partial write safe: the form wants every port's
// value on each submit, so the ones this driver is not asked to change have to
// be sent back exactly as they were.
var (
	allInfoStateRE  = regexp.MustCompile(`state\s*:\s*\[([0-9,\s]*)\]`)
	allInfoSpeedRE  = regexp.MustCompile(`spd_cfg\s*:\s*\[([0-9,\s]*)\]`)
	allInfoFlowRE   = regexp.MustCompile(`fc_cfg\s*:\s*\[([0-9,\s]*)\]`)
	maxPortRE       = regexp.MustCompile(`var\s+max_port_num\s*=\s*(\d+)`)
	loopPreventRE   = regexp.MustCompile(`var\s+lpEn\s*=\s*\[\s*(\d+)\s*\]`)
	stormEnabledRE  = regexp.MustCompile(`var\s+sc_en\s*=\s*(\d+)`)
	stormRateRE     = regexp.MustCompile(`var\s+pps\s*=\s*(\d+)`)
	portStatusRE    = regexp.MustCompile(`var\s+portStatus\s*=\s*"([^"]*)"`)
	dataInfoFieldRE = regexp.MustCompile(`(\w+)\s*:\s*\[\s*"([^"]*)"\s*\]`)
)

// Speed is a port's configured link speed, in the words a person would use.
// The firmware stores a small integer; these are the six it accepts.
const (
	SpeedAuto     = "auto"
	Speed1000Full = "1000-full"
	Speed100Auto  = "100-auto"
	Speed100Full  = "100-full"
	Speed10Auto   = "10-auto"
	Speed10Full   = "10-full"
)

var speedToCode = map[string]int{
	SpeedAuto: 0, Speed1000Full: 1, Speed100Auto: 2,
	Speed100Full: 3, Speed10Auto: 4, Speed10Full: 5,
}

var codeToSpeed = map[int]string{
	0: SpeedAuto, 1: Speed1000Full, 2: Speed100Auto,
	3: Speed100Full, 4: Speed10Auto, 5: Speed10Full,
}

// Speeds lists the accepted values, for schema validation and error messages.
func Speeds() []string {
	names := make([]string, 0, len(speedToCode))
	for name := range speedToCode {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// PortSettings is one port's electrical configuration, as opposed to its
// 802.1Q membership.
type PortSettings struct {
	Port        int
	Enabled     bool
	Speed       string
	FlowControl bool
}

// SwitchSettings is every port's settings plus the switch-wide knobs that
// live on the same page.
type SwitchSettings struct {
	Ports []PortSettings
	// LoopPrevention and the storm-control pair are read here because the
	// firmware puts them on the port page, not because they belong to a port.
	LoopPrevention bool
	StormControl   bool
	StormRatePPS   int
}

// Port returns one port's settings, and whether the switch has that port.
func (s SwitchSettings) Port(port int) (PortSettings, bool) {
	for _, p := range s.Ports {
		if p.Port == port {
			return p, true
		}
	}
	return PortSettings{}, false
}

func parseIntList(match []string) []int {
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
			values = append(values, value)
		}
	}
	return values
}

func parsePortPage(html string) (SwitchSettings, error) {
	state := parseIntList(allInfoStateRE.FindStringSubmatch(html))
	speed := parseIntList(allInfoSpeedRE.FindStringSubmatch(html))
	flow := parseIntList(allInfoFlowRE.FindStringSubmatch(html))
	if len(state) == 0 {
		return SwitchSettings{}, fmt.Errorf(
			"the port page carried no all_info block; the firmware may differ from " +
				"the V1.00(ACPS.2)C0 this driver was written against")
	}

	count := len(state)
	if match := maxPortRE.FindStringSubmatch(html); match != nil {
		if value, err := strconv.Atoi(match[1]); err == nil && value > 0 && value < count {
			count = value
		}
	}

	settings := SwitchSettings{LoopPrevention: true}
	for i := 0; i < count; i++ {
		port := PortSettings{Port: i + 1, Enabled: state[i] != 0, Speed: SpeedAuto}
		if i < len(speed) {
			if name, ok := codeToSpeed[speed[i]]; ok {
				port.Speed = name
			}
		}
		if i < len(flow) {
			port.FlowControl = flow[i] != 0
		}
		settings.Ports = append(settings.Ports, port)
	}

	if match := loopPreventRE.FindStringSubmatch(html); match != nil {
		settings.LoopPrevention = match[1] != "0"
	}
	if match := stormEnabledRE.FindStringSubmatch(html); match != nil {
		settings.StormControl = match[1] != "0"
	}
	if match := stormRateRE.FindStringSubmatch(html); match != nil {
		settings.StormRatePPS, _ = strconv.Atoi(match[1])
	}
	return settings, nil
}

// readSettings fetches the port page. The session must be held.
func (c *Client) readSettings(ctx context.Context) (SwitchSettings, error) {
	html, err := c.get(ctx, "zPort.html")
	if err != nil {
		return SwitchSettings{}, err
	}
	return parsePortPage(html)
}

// ReadSettings reads every port's electrical configuration.
func (c *Client) ReadSettings(ctx context.Context) (SwitchSettings, error) {
	if cached, ok := c.cachedSettings(); ok {
		return cached, nil
	}
	if c.Password == "" {
		return SwitchSettings{}, fmt.Errorf(
			"%w: port settings live behind the login", ErrAuth)
	}

	var settings SwitchSettings
	err := c.withSession(ctx, func(ctx context.Context) error {
		var err error
		settings, err = c.readSettings(ctx)
		return err
	})
	if err == nil {
		c.cacheSettings(settings)
	}
	return settings, err
}

// WritePortSettings applies one port's electrical configuration.
//
// The form wants every port's value on each submit, so the others are read
// and sent back unchanged, and `g_port_map` names the single port that is
// actually meant to move. Two ports can therefore be written independently,
// the same invariant that makes the 802.1Q side composable.
func (c *Client) WritePortSettings(ctx context.Context, want PortSettings, force bool) (SwitchSettings, error) {
	c.forget()

	if _, ok := speedToCode[want.Speed]; !ok {
		return SwitchSettings{}, fmt.Errorf("unknown speed %q; accepted values are %s",
			want.Speed, strings.Join(Speeds(), ", "))
	}

	var result SwitchSettings
	err := c.withSession(ctx, func(ctx context.Context) error {
		current, err := c.readSettings(ctx)
		if err != nil {
			return err
		}
		existing, exists := current.Port(want.Port)
		if !exists {
			return fmt.Errorf("port %d does not exist on this switch, which has %d",
				want.Port, len(current.Ports))
		}
		if existing == want {
			result = current
			return nil
		}

		if !want.Enabled && existing.Enabled {
			config, err := c.readState(ctx)
			if err != nil {
				return err
			}
			if err := guardPortShutdown(config, want.Port, force); err != nil {
				return err
			}
		}

		form := url.Values{}
		state, flow := 0, 0
		for _, port := range current.Ports {
			settings := port
			if port.Port == want.Port {
				settings = want
			}
			if settings.Enabled {
				state |= 1 << uint(port.Port-1)
			}
			if settings.FlowControl {
				flow |= 1 << uint(port.Port-1)
			}
		}
		// The page emits eight speed fields whatever the model; the ones past
		// the last port stay at -1, which is how the firmware reads "leave it".
		for i := 0; i < 8; i++ {
			value := "-1"
			if i < len(current.Ports) {
				settings := current.Ports[i]
				if settings.Port == want.Port {
					settings = want
				}
				value = strconv.Itoa(speedToCode[settings.Speed])
			}
			form.Set(fmt.Sprintf("g_port_speed%d", i), value)
		}
		form.Set("g_port_state", strconv.Itoa(state))
		form.Set("g_port_flwcl", strconv.Itoa(flow))
		// Only this port's bit: the firmware applies the change to the ports
		// named here and leaves the rest alone.
		form.Set("g_port_map", strconv.Itoa(1<<uint(want.Port-1)))
		// This model has no PoE; -1 is what the page sends for it.
		form.Set("g_port_map_poe", "-1")
		form.Set("g_port_poe", "-1")

		if _, err := c.fetch(ctx, "POST", "zport_setting.cgi", form.Encode(),
			"application/x-www-form-urlencoded"); err != nil {
			return err
		}

		after, err := c.readSettings(ctx)
		if err != nil {
			return err
		}
		got, _ := after.Port(want.Port)
		if got != want {
			return fmt.Errorf("port %d settings did not apply as requested: asked for "+
				"enabled=%t speed=%s flow_control=%t, switch reports "+
				"enabled=%t speed=%s flow_control=%t",
				want.Port, want.Enabled, want.Speed, want.FlowControl,
				got.Enabled, got.Speed, got.FlowControl)
		}
		result = after
		return nil
	})
	return result, err
}

// guardPortShutdown refuses to switch off a port carrying the management VLAN.
//
// The switch is reachable through whichever port carries that VLAN. Shutting
// it takes the web interface away, and getting it back means physical access.
func guardPortShutdown(config Config, port int, force bool) error {
	if force || config.ManagementVLAN == 0 {
		return nil
	}
	entry, exists := config.VLAN(config.ManagementVLAN)
	if !exists {
		return nil
	}
	if entry.Members()[port] {
		return fmt.Errorf("%w: port %d carries the management VLAN %d; switching it "+
			"off would take the switch off the network",
			ErrUnsafe, port, config.ManagementVLAN)
	}
	return nil
}

// -- device identity, readable without a session ---------------------------

// DeviceInfo is what the login page volunteers about the switch. No session is
// needed, which is a firmware weakness rather than a gift, but it means a
// refresh of this costs the owner nothing.
type DeviceInfo struct {
	Name     string
	Model    string
	Hardware string
	Firmware string
	MAC      string
	IP       string
	Netmask  string
	Gateway  string
	DNS      string
	UptimeS  int
}

// ReadDeviceInfo parses the data_info block on the login page.
func (c *Client) ReadDeviceInfo(ctx context.Context) (DeviceInfo, error) {
	lock := lockFor(c.Host)
	lock.Lock()
	defer lock.Unlock()

	html, err := c.get(ctx, "zlogin.html")
	if err != nil {
		return DeviceInfo{}, err
	}

	fields := map[string]string{}
	for _, match := range dataInfoFieldRE.FindAllStringSubmatch(html, -1) {
		fields[match[1]] = strings.TrimSpace(match[2])
	}
	info := DeviceInfo{
		Name:     fields["sysnameStr"],
		Model:    fields["modelStr"],
		Hardware: fields["hardwareStr"],
		Firmware: fields["firmwareStr"],
		MAC:      fields["macStr"],
		IP:       fields["ipStr"],
		Netmask:  fields["netmaskStr"],
		Gateway:  fields["gatewayStr"],
		DNS:      fields["dnsStr"],
	}
	// system_uptime is a bare number rather than a quoted string.
	if match := regexp.MustCompile(`system_uptime\s*:\s*\[\s*"?(\d+)"?\s*\]`).
		FindStringSubmatch(html); match != nil {
		info.UptimeS, _ = strconv.Atoi(match[1])
	}
	return info, nil
}

// LinkStatus is a port's live electrical state, as opposed to its
// configuration: whether something is plugged in, and at what rate.
type LinkStatus struct {
	Port    int
	Up      bool
	SpeedMB int // 0 when the link is down
}

// ReadLinkStatus reads portStatus.xml, which needs no session either.
//
// The endpoint serves four groups of per-port values separated by "&". The
// first is link state and the second the negotiated rate; both were checked
// against SNMP readings on all ten ports of the fleet. The remaining two are
// not decoded, because guessing at them would put invented facts in a data
// source people would then trust.
func (c *Client) ReadLinkStatus(ctx context.Context) ([]LinkStatus, error) {
	lock := lockFor(c.Host)
	lock.Lock()
	defer lock.Unlock()

	payload, err := c.get(ctx, "portStatus.xml")
	if err != nil {
		return nil, err
	}
	return parseLinkStatus(payload), nil
}

// rateFromCode maps the second group's value. 1 appears on ports whose link is
// down, where it means nothing.
var rateFromCode = map[int]int{2: 100, 3: 1000}

func parseLinkStatus(payload string) []LinkStatus {
	match := portStatusRE.FindStringSubmatch(payload)
	if match == nil {
		return nil
	}
	groups := strings.Split(match[1], "&")
	if len(groups) < 2 {
		return nil
	}

	value := func(group, index int) int {
		fields := strings.Split(strings.TrimSuffix(groups[group], ","), ",")
		if index >= len(fields) {
			return 0
		}
		n, _ := strconv.Atoi(strings.TrimSpace(fields[index]))
		return n
	}

	count := len(strings.Split(strings.TrimSuffix(groups[0], ","), ","))
	status := make([]LinkStatus, 0, count)
	for i := 0; i < count; i++ {
		entry := LinkStatus{Port: i + 1, Up: value(0, i) != 0}
		if entry.Up {
			entry.SpeedMB = rateFromCode[value(1, i)]
		}
		status = append(status, entry)
	}
	return status
}

// -- device name -----------------------------------------------------------

// deviceNameRE is the firmware's own validation, copied rather than invented:
// the page refuses anything else before submitting.
var deviceNameRE = regexp.MustCompile(`^[\w\d\-]{1,14}$`)

// WriteDeviceName renames the switch.
func (c *Client) WriteDeviceName(ctx context.Context, name string) error {
	c.forget()
	if !deviceNameRE.MatchString(name) {
		return fmt.Errorf("device name %q is not acceptable to this firmware: "+
			"1 to 14 characters, letters, digits, underscore or hyphen", name)
	}

	return c.withSession(ctx, func(ctx context.Context) error {
		form := url.Values{"sysName": {name}}
		if _, err := c.fetch(ctx, "POST", "zsystem_name_set.cgi", form.Encode(),
			"application/x-www-form-urlencoded"); err != nil {
			return err
		}
		// The CGI answers the same whatever it did, so the name is read back.
		html, err := c.get(ctx, "zlogin.html")
		if err != nil {
			return err
		}
		for _, match := range dataInfoFieldRE.FindAllStringSubmatch(html, -1) {
			if match[1] == "sysnameStr" && strings.TrimSpace(match[2]) != name {
				return fmt.Errorf("the switch still reports the name %q after being "+
					"asked for %q", strings.TrimSpace(match[2]), name)
			}
		}
		return nil
	})
}

// -- loop prevention and storm control -------------------------------------

// WriteProtection sets loop prevention and storm control, which the firmware
// puts on one form even though they are different mechanisms.
//
// Loop prevention is what saves a switch from a cable plugged into itself;
// storm control caps broadcast floods at a packet rate. Both are sent together
// because the form wants all three fields on every submit.
func (c *Client) WriteProtection(ctx context.Context, loopPrevention, stormControl bool, stormRatePPS int, force bool) (SwitchSettings, error) {
	c.forget()

	if stormControl && (stormRatePPS < 1 || stormRatePPS > 500000) {
		return SwitchSettings{}, fmt.Errorf(
			"storm control rate %d is outside the range the firmware accepts (1 to 500000)",
			stormRatePPS)
	}
	if !loopPrevention && !force {
		return SwitchSettings{}, fmt.Errorf("%w: switching loop prevention off leaves the "+
			"switch defenceless against a cable plugged back into itself, which floods "+
			"the segment and is not recoverable remotely", ErrUnsafe)
	}

	var result SwitchSettings
	err := c.withSession(ctx, func(ctx context.Context) error {
		form := url.Values{
			"lpEn":           {boolField(loopPrevention)},
			"storm_ctrl_en":  {boolField(stormControl)},
			"storm_ctrl_pps": {strconv.Itoa(stormRatePPS)},
		}
		if _, err := c.fetch(ctx, "POST", "zloop_prevention_set.cgi", form.Encode(),
			"application/x-www-form-urlencoded"); err != nil {
			return err
		}

		after, err := c.readSettings(ctx)
		if err != nil {
			return err
		}
		if after.LoopPrevention != loopPrevention || after.StormControl != stormControl {
			return fmt.Errorf("the switch did not apply the protection settings: asked "+
				"for loop_prevention=%t storm_control=%t, it reports %t and %t",
				loopPrevention, stormControl, after.LoopPrevention, after.StormControl)
		}
		result = after
		return nil
	})
	return result, err
}

func boolField(on bool) string {
	if on {
		return "1"
	}
	return "0"
}
