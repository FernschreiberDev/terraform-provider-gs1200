// Package fakeswitch is a GS1200-5 v3 emulated closely enough to drive the
// real provider against it.
//
// It exists because the interesting behaviour of this hardware is not the
// happy path — it is the single web session, the CGI endpoints that answer 200
// whether or not they applied anything, and the port bitmaps offset by one. A
// unit test can assert the shape of a request; only a stateful device can
// prove that a write, a read-back and a plan agree.
//
// It is deliberately faithful where being unfaithful would hide a bug, and
// deliberately simple everywhere else: there is no firmware upgrade, no port
// statistics, and no trunking.
package fakeswitch

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// VLAN is one row of the device's VLAN table.
type VLAN struct {
	Index    int
	VID      int
	Name     string
	Tagged   []int
	Untagged []int
}

// Switch holds the emulated device's state.
type Switch struct {
	mu sync.Mutex

	// PasswordSHA256 is the hex digest the login page would submit. The
	// firmware never sees the plaintext, and neither does this.
	PasswordSHA256 string
	PortCount      int
	ManagementVLAN int
	VLANEnabled    bool
	Model          string
	Firmware       string

	vlans []VLAN
	pvid  map[int]int

	// Per-port electrical settings, the other half of what the device holds.
	SysName string
	// Protections à l'échelle de l'appareil, que le firmware sert sur la même
	// page que les ports.
	loopPrevention bool
	stormControl   bool
	stormRatePPS   int
	portEnabled    []bool
	portSpeed      []int
	portFlow       []bool

	// token is the single session. Empty means nobody holds it, which is what
	// makes a missed logout visible instead of merely slow.
	token string
	// Calls records every request path, so a test can assert that a write
	// released the session.
	Calls []string
}

// Seed replaces the emulated device's VLAN table and PVIDs. It takes the
// table in the wire format /vlanEntry.xml serves, so a capture from real
// hardware can be replayed verbatim.
func (s *Switch) Seed(vlanXML string, pvid map[int]int) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.vlans = nil
	for _, record := range strings.Split(vlanXML, ";") {
		fields := strings.Split(strings.TrimSpace(record), ",")
		if len(fields) < 6 {
			continue
		}
		s.vlans = append(s.vlans, VLAN{
			Index: atoi(fields[0]), VID: atoi(fields[1]), Name: fields[2],
			Tagged: decode(atoi(fields[4])), Untagged: decode(atoi(fields[5])),
		})
	}
	s.pvid = map[int]int{}
	for port, vid := range pvid {
		s.pvid[port] = vid
	}
	s.PortCount = len(s.pvid)
}

// New builds a switch seeded with the configuration captured from the real
// gs1200 at 192.168.2.6.
func New(password string) *Switch {
	sum := sha256.Sum256([]byte(password))
	return &Switch{
		PasswordSHA256: hex.EncodeToString(sum[:]),
		PortCount:      5,
		ManagementVLAN: 1,
		VLANEnabled:    true,
		Model:          "GS1200-5v3",
		Firmware:       "V1.00(ACPS.2)C0",
		SysName:        "Gaming",
		loopPrevention: true,
		portEnabled:    []bool{true, true, true, true, true},
		portSpeed:      []int{0, 0, 0, 0, 0},
		portFlow:       []bool{false, false, false, false, false},
		vlans: []VLAN{
			{Index: 1, VID: 1, Name: "1", Untagged: []int{1, 2}},
			{Index: 2, VID: 8, Name: "8", Tagged: []int{1}, Untagged: []int{5}},
			{Index: 3, VID: 1003, Name: "1003", Tagged: []int{1, 2}, Untagged: []int{3, 4}},
		},
		pvid: map[int]int{1: 1, 2: 1, 3: 1003, 4: 1003, 5: 8},
	}
}

// VLANs returns a copy of the current table, for assertions.
func (s *Switch) VLANs() []VLAN {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]VLAN(nil), s.vlans...)
}

// PVIDs returns a copy of the current PVID map, for assertions.
func (s *Switch) PVIDs() map[int]int {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[int]int, len(s.pvid))
	for port, vid := range s.pvid {
		out[port] = vid
	}
	return out
}

// SessionHeld reports whether a login is still outstanding. A test that
// finishes with this true has found a driver that locks the owner out.
func (s *Switch) SessionHeld() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.token != ""
}

func decode(bitmap int) []int {
	shifted := bitmap >> 1
	ports := []int{}
	for i := 0; i < 32; i++ {
		if shifted&(1<<uint(i)) != 0 {
			ports = append(ports, i+1)
		}
	}
	return ports
}

func encode(ports []int) int {
	value := 0
	for _, port := range ports {
		if port >= 1 {
			value |= 1 << uint(port)
		}
	}
	return value
}

// Handler is the device's web interface.
func (s *Switch) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/vlanEntry.xml", s.vlanEntry)
	mux.HandleFunc("/zVLAN_1Q_List.html", s.listPage)
	mux.HandleFunc("/zlogin.html", s.loginPage)
	mux.HandleFunc("/logon.cgi", s.logon)
	mux.HandleFunc("/zlogout.cgi", s.logout)
	mux.HandleFunc("/zqvlanSet.cgi", s.vlanSet)
	mux.HandleFunc("/zqvlanPvidSet.cgi", s.pvidSet)
	mux.HandleFunc("/zPort.html", s.portPage)
	mux.HandleFunc("/zport_setting.cgi", s.portSet)
	mux.HandleFunc("/zsystem_name_set.cgi", s.nameSet)
	mux.HandleFunc("/portStatus.xml", s.portStatus)
	mux.HandleFunc("/zloop_prevention_set.cgi", s.protectionSet)
	return s.record(mux)
}

func (s *Switch) record(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.Calls = append(s.Calls, r.URL.Path)
		s.mu.Unlock()
		next.ServeHTTP(w, r)
	})
}

// vlanEntry serves the table to anyone who asks. This endpoint really is
// unauthenticated on the hardware; reproducing that is the point.
func (s *Switch) vlanEntry(w http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out strings.Builder
	for _, vlan := range s.vlans {
		members := encode(vlan.Tagged) | encode(vlan.Untagged)
		fmt.Fprintf(&out, "%d,%d,%s,%d,%d,%d;",
			vlan.Index, vlan.VID, vlan.Name, members,
			encode(vlan.Tagged), encode(vlan.Untagged))
	}
	_, _ = w.Write([]byte(out.String()))
}

// listPage needs a session, like the real one.
func (s *Switch) listPage(w http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.token == "" {
		// The firmware redirects to the login rather than serving the page.
		http.Redirect(w, &http.Request{}, "/zlogin.html", http.StatusFound)
		return
	}
	ports := make([]string, 0, s.PortCount)
	for port := 1; port <= s.PortCount; port++ {
		ports = append(ports, strconv.Itoa(s.pvid[port]))
	}
	state := 1
	if !s.VLANEnabled {
		state = 0
	}
	fmt.Fprintf(w, `<script>
var portMaxNum = %d;
var vlanState = %d;
var pvids = [%s];
var sysObj = { mgmt_vlan : [ '%d' ] };
</script>`, s.PortCount, state, strings.Join(ports, ","), s.ManagementVLAN)
}

func (s *Switch) loginPage(w http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	errType := "1"
	if s.token != "" {
		errType = "2"
	}
	model, firmware, name := s.Model, s.Firmware, s.SysName
	s.mu.Unlock()
	// The real page volunteers all of this without a session.
	fmt.Fprintf(w, `<script>
var errType = "%s";
var sysObj = { modelStr : [ "%s" ], firmwareStr : [ "%s" ] };
var data_info = {sysnameStr:["%s"],modelStr:["%s"], macStr:["c4:9a:31:46:eb:23"],
ipStr:["192.168.2.6"],netmaskStr:["255.255.255.0"],gatewayStr:["192.168.2.1"],
dnsStr:["----"],firmwareStr:["%s"], system_uptime:["652992"], hardwareStr:["AN8858"]};
</script>`, errType, model, firmware, name, model, firmware)
}

// portPage imitates zPort.html: the whole per-port configuration in one
// JavaScript object, which is how the firmware serves it.
func (s *Switch) portPage(w http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.token == "" {
		http.Redirect(w, &http.Request{}, "/zlogin.html", http.StatusFound)
		return
	}

	join := func(values []string) string { return strings.Join(values, ",") }
	state, speed, flow := []string{}, []string{}, []string{}
	for i := range s.portEnabled {
		state = append(state, boolDigit(s.portEnabled[i]))
		speed = append(speed, strconv.Itoa(s.portSpeed[i]))
		flow = append(flow, boolDigit(s.portFlow[i]))
	}
	fmt.Fprintf(w, `<script>
var max_port_num=%d;
var lpEn = [%s];
var sc_en = %s;
var pps = %d;
all_info = {
state:[%s,],
spd_cfg:[%s,],
mode_cfg:[0,0,0,0,0,],
fc_cfg:[%s,],
ability:[31,31,31,31,31,],
trunk_info:[0,0,0,0,0,],
}
</script>`, s.PortCount, boolDigit(s.loopPrevention), boolDigit(s.stormControl),
		s.stormRatePPS, join(state), join(speed), join(flow))
}

// protectionSet imitates zloop_prevention_set.cgi, which carries loop
// prevention and storm control on one form.
func (s *Switch) protectionSet(w http.ResponseWriter, r *http.Request) {
	defer w.WriteHeader(http.StatusOK)
	_ = r.ParseForm()

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.token == "" {
		return
	}
	if v := r.PostForm.Get("lpEn"); v != "" {
		s.loopPrevention = v != "0"
	}
	if v := r.PostForm.Get("storm_ctrl_en"); v != "" {
		s.stormControl = v != "0"
	}
	if v := r.PostForm.Get("storm_ctrl_pps"); v != "" {
		s.stormRatePPS = atoi(v)
	}
}

// portSet imitates zport_setting.cgi. It honours g_port_map: only the ports
// named there change, which is what lets one port be written without
// disturbing the others.
func (s *Switch) portSet(w http.ResponseWriter, r *http.Request) {
	defer w.WriteHeader(http.StatusOK)
	_ = r.ParseForm()

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.token == "" {
		return
	}

	changed := atoi(r.PostForm.Get("g_port_map"))
	state := atoi(r.PostForm.Get("g_port_state"))
	flow := atoi(r.PostForm.Get("g_port_flwcl"))
	for i := range s.portEnabled {
		if changed&(1<<uint(i)) == 0 {
			continue
		}
		s.portEnabled[i] = state&(1<<uint(i)) != 0
		s.portFlow[i] = flow&(1<<uint(i)) != 0
		if raw := r.PostForm.Get(fmt.Sprintf("g_port_speed%d", i)); raw != "" && raw != "-1" {
			s.portSpeed[i] = atoi(raw)
		}
	}
}

func (s *Switch) nameSet(w http.ResponseWriter, r *http.Request) {
	defer w.WriteHeader(http.StatusOK)
	_ = r.ParseForm()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.token == "" {
		return
	}
	if name := r.PostForm.Get("sysName"); name != "" {
		s.SysName = name
	}
}

// portStatus imitates portStatus.xml: four groups of per-port values, of which
// the first two are link state and negotiated rate.
func (s *Switch) portStatus(w http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var up, rate, third, fourth []string
	for i := range s.portEnabled {
		// A port is "linked" here when it is administratively up; the
		// emulator has no cables to model.
		up = append(up, boolDigit(s.portEnabled[i]))
		if s.portEnabled[i] {
			rate = append(rate, "3")
		} else {
			rate = append(rate, "1")
		}
		third = append(third, "1")
		fourth = append(fourth, "0")
	}
	fmt.Fprintf(w, `<script> var portStatus = "%s,&%s,&%s,&%s,&";</script>`,
		strings.Join(up, ","), strings.Join(rate, ","),
		strings.Join(third, ","), strings.Join(fourth, ","))
}

func boolDigit(on bool) string {
	if on {
		return "1"
	}
	return "0"
}

func (s *Switch) logon(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	s.mu.Lock()
	defer s.mu.Unlock()

	// One session at a time: a second login is refused even with the right
	// password, which is the behaviour that makes a missed logout painful.
	if s.token != "" || r.PostForm.Get("password") != s.PasswordSHA256 {
		_, _ = w.Write([]byte("<script>var allow = 0;</script>"))
		return
	}
	s.token = "SESSION" + strconv.Itoa(len(s.Calls))
	http.SetCookie(w, &http.Cookie{Name: "Cookies", Value: s.token})
	_, _ = w.Write([]byte("<script>var allow = 1;</script>"))
}

// logout only accepts the firmware's own shape: a POST whose text/plain body
// carries the session token. A GET, or a POST without the token, frees nothing.
func (s *Switch) logout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusOK)
		return
	}
	buf := make([]byte, 256)
	n, _ := r.Body.Read(buf)
	body := strings.TrimSpace(string(buf[:n]))

	s.mu.Lock()
	defer s.mu.Unlock()
	if body == "Cookies="+s.token {
		s.token = ""
	}
	w.WriteHeader(http.StatusOK)
}

// vlanSet answers 200 whatever happens, exactly like the firmware — which is
// why the driver has to read the table back to know anything.
func (s *Switch) vlanSet(w http.ResponseWriter, r *http.Request) {
	defer w.WriteHeader(http.StatusOK)
	q := r.URL.Query()

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.token == "" {
		return // no session: silently ignored
	}

	vid := atoi(q.Get("vid"))
	tagged := decode(atoi(q.Get("tagMbrs")))
	untagged := decode(atoi(q.Get("untagMbrs")))

	switch q.Get("action") {
	case "add":
		s.vlans = append(s.vlans, VLAN{
			Index: atoi(q.Get("vidx")), VID: vid, Name: q.Get("name"),
			Tagged: tagged, Untagged: untagged,
		})
		sort.Slice(s.vlans, func(i, j int) bool { return s.vlans[i].Index < s.vlans[j].Index })
	case "mod":
		for i := range s.vlans {
			if s.vlans[i].VID == vid {
				s.vlans[i].Tagged = tagged
				s.vlans[i].Untagged = untagged
			}
		}
	case "del":
		// The firmware refuses to delete a VLAN still used as a PVID.
		for _, pv := range s.pvid {
			if pv == vid {
				return
			}
		}
		kept := s.vlans[:0]
		for _, vlan := range s.vlans {
			if vlan.VID != vid {
				kept = append(kept, vlan)
			}
		}
		s.vlans = kept
	}
}

func (s *Switch) pvidSet(w http.ResponseWriter, r *http.Request) {
	defer w.WriteHeader(http.StatusOK)
	q := r.URL.Query()

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.token == "" {
		return
	}
	known := map[int]bool{}
	for _, vlan := range s.vlans {
		known[vlan.VID] = true
	}
	for port := 1; port <= s.PortCount; port++ {
		// The firmware indexes these from zero.
		raw := q.Get("vid" + strconv.Itoa(port-1))
		if raw == "" {
			continue
		}
		if vid := atoi(raw); known[vid] {
			s.pvid[port] = vid
		}
	}
}

func atoi(value string) int {
	n, _ := strconv.Atoi(strings.TrimSpace(value))
	return n
}
