package fakeswitch

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
)

// The switch-wide pages, kept apart from the VLAN plumbing because the
// firmware scatters these settings across three of them.
func (s *Switch) registerManagement(mux *http.ServeMux) {
	mux.HandleFunc("/zManagement.html", s.managementPage)
	mux.HandleFunc("/zIGMP_Snooping.html", s.igmpPage)
	mux.HandleFunc("/zadvenced_set_modify.html", s.advancedPage)
	mux.HandleFunc("/zeeeSet.cgi", s.eeeSet)
	mux.HandleFunc("/led_cfg.cgi", s.ledSet)
	mux.HandleFunc("/zsnmp.cgi", s.snmpSet)
	mux.HandleFunc("/zigmpSnooping.cgi", s.igmpSet)
	mux.HandleFunc("/zmvlanSet.cgi", s.isolationSet)
	mux.HandleFunc("/zrate_limit_set.cgi", s.rateSet)
}

// authorised answers the pages only to a holder of the single session, which
// is what the firmware does.
func (s *Switch) authorised(w http.ResponseWriter) bool {
	if s.token == "" {
		http.Redirect(w, &http.Request{}, "/zlogin.html", http.StatusFound)
		return false
	}
	return true
}

func (s *Switch) managementPage(w http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.authorised(w) {
		return
	}
	eeeBit := 0x0
	if s.eee {
		eeeBit = 0x1f
	}
	fmt.Fprintf(w, `<script>
var eeeinfo_ds = { portNum:%d, enable_bit:0x%x,};
var led_eco = %s;
ip_ds={state:0,vlan:1,maxVlan:4094,ipStr:['192.168.2.6'],mgmt_vlan:['%d'],}
snmp_info={snmpv1:%s,snmpv2:%s,readCm:["public"],}
</script>`, s.PortCount, eeeBit, boolDigit(s.led), s.mgmtVLAN,
		boolDigit(s.snmp), boolDigit(s.snmp))
}

func (s *Switch) igmpPage(w http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.authorised(w) {
		return
	}
	router := 0
	if s.igmpRouter > 0 {
		router = 1 << uint(s.igmpRouter-1)
	}
	fmt.Fprintf(w, `<script>
igmp_ds = {state:%s,fastleaveState:1,umcdropState:%s, vids:[1,8,1003,],rstic:%d,rdyna:0}
</script>`, boolDigit(s.igmpSnooping), boolDigit(s.igmpDrop), router)
}

func (s *Switch) advancedPage(w http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.authorised(w) {
		return
	}
	join := func(values []int) string {
		out := make([]string, len(values))
		for i, v := range values {
			out[i] = strconv.Itoa(v)
		}
		return strings.Join(out, ",")
	}
	isolation := 2046
	if s.portIsolation > 0 {
		isolation = 1 << uint(s.portIsolation)
	}
	fmt.Fprintf(w, `<script>
var port_iso = %d;
portspeed_info={ingress:[%s,], egress:[%s,]}
</script>`, isolation, join(s.ingressUnits), join(s.egressUnits))
}

func (s *Switch) eeeSet(w http.ResponseWriter, r *http.Request) {
	defer w.WriteHeader(http.StatusOK)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.token == "" {
		return
	}
	if v := r.URL.Query().Get("state"); v != "" {
		s.eee = v != "0"
	}
}

func (s *Switch) ledSet(w http.ResponseWriter, r *http.Request) {
	defer w.WriteHeader(http.StatusOK)
	_ = r.ParseForm()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.token == "" {
		return
	}
	if v := r.PostForm.Get("led_state_f"); v != "" && v != "-1" {
		s.led = v != "0"
	}
}

func (s *Switch) snmpSet(w http.ResponseWriter, r *http.Request) {
	defer w.WriteHeader(http.StatusOK)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.token == "" {
		return
	}
	if v := r.URL.Query().Get("snmpv2"); v != "" {
		s.snmp = v != "0"
	}
}

func (s *Switch) igmpSet(w http.ResponseWriter, r *http.Request) {
	defer w.WriteHeader(http.StatusOK)
	q := r.URL.Query()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.token == "" {
		return
	}
	if v := q.Get("igmp_mode"); v != "" {
		s.igmpSnooping = v != "0"
	}
	if v := q.Get("MCstate"); v != "" {
		s.igmpDrop = v != "0"
	}
	if v := q.Get("mrouter"); v != "" {
		bitmap := atoi(v)
		s.igmpRouter = 0
		for i := 0; i < 32; i++ {
			if bitmap&(1<<uint(i)) != 0 {
				s.igmpRouter = i + 1
				break
			}
		}
	}
}

func (s *Switch) isolationSet(w http.ResponseWriter, r *http.Request) {
	defer w.WriteHeader(http.StatusOK)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.token == "" {
		return
	}
	if v := r.URL.Query().Get("portbmp"); v != "" {
		bitmap := atoi(v)
		s.portIsolation = 0
		if bitmap != 2046 {
			for i := 0; i < 32; i++ {
				if bitmap&(1<<uint(i)) != 0 {
					s.portIsolation = i
					break
				}
			}
		}
	}
}

// rateSet honours g_selMask: only the ports named there move, which is what
// lets one port be capped without disturbing the others.
func (s *Switch) rateSet(w http.ResponseWriter, r *http.Request) {
	defer w.WriteHeader(http.StatusOK)
	_ = r.ParseForm()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.token == "" {
		return
	}

	split := func(raw string) []int {
		out := []int{}
		for _, field := range strings.Split(strings.TrimSuffix(raw, "_"), "_") {
			if field == "" {
				continue
			}
			out = append(out, atoi(field))
		}
		return out
	}
	ingress := split(r.PostForm.Get("g_ingressSpeed"))
	egress := split(r.PostForm.Get("g_egressSpeed"))
	changed := atoi(r.PostForm.Get("g_selMask"))

	for i := range s.ingressUnits {
		if changed&(1<<uint(i)) == 0 {
			continue
		}
		if i < len(ingress) {
			s.ingressUnits[i] = ingress[i]
		}
		if i < len(egress) {
			s.egressUnits[i] = egress[i]
		}
	}
}
