package zyxel

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"
)

// Fixtures are the exact bytes served by a GS1200-5v3 on V1.00(ACPS.2)C0, so a
// failure here means the decoding drifted from the hardware rather than from an
// assumption about it. The bit-shift is the dangerous part: the device puts
// port 1 at bit 1, and being one out silently moves every port by one position
// — on a write, that reassigns live traffic to the wrong physical socket.
const realVLANXML = "1,1,1,0,0,6;2,8,8,0,2,32;3,1003,1003,0,6,24;"

// Captured from the same switch's zVLAN_1Q_List.html.
var capturedPVIDs = []int{1, 1, 1003, 1003, 8}

const realListPage = `<script>
var portMaxNum = 5;
var vlanState = 1;
var pvids = [1,1,1003,1003,8];
var sysObj = { mgmt_vlan : [ '1' ] };
</script>`

// -- port bitmaps ----------------------------------------------------------

func TestPortOneIsBitOne(t *testing.T) {
	// The device offsets by one: 2 means port 1, not port 2.
	assertPorts(t, DecodePorts(2), []int{1})
	assertPorts(t, DecodePorts(1), []int{}) // bit 0 is unused
	assertPorts(t, DecodePorts(4), []int{2})
}

func TestRealCapturedBitmaps(t *testing.T) {
	assertPorts(t, DecodePorts(6), []int{1, 2})  // VLAN 1 untagged
	assertPorts(t, DecodePorts(32), []int{5})    // VLAN 8 untagged
	assertPorts(t, DecodePorts(24), []int{3, 4}) // VLAN 1003 untagged
}

func TestEncodeIsTheExactInverse(t *testing.T) {
	for _, bitmap := range []int{2, 6, 24, 32, 62, 0} {
		if got := EncodePorts(DecodePorts(bitmap)); got != bitmap {
			t.Errorf("round trip of %d gave %d", bitmap, got)
		}
	}
}

func TestEncodeMatchesTheFirmwareShift(t *testing.T) {
	// zqvlan_modify.html computes tagBmp = tpbmp << 1 where tpbmp has port 1
	// at bit 0. Port 1 must therefore encode to 2.
	for _, tc := range []struct {
		ports []int
		want  int
	}{
		{[]int{1}, 2},
		{[]int{1, 2}, 6},
		{[]int{5}, 32},
		{nil, 0},
	} {
		if got := EncodePorts(tc.ports); got != tc.want {
			t.Errorf("EncodePorts(%v) = %d, want %d", tc.ports, got, tc.want)
		}
	}
}

// -- parsing ---------------------------------------------------------------

func TestParsesTheRealTable(t *testing.T) {
	entries := ParseVLANEntries(realVLANXML)
	if len(entries) != 3 {
		t.Fatalf("got %d entries, want 3", len(entries))
	}
	if got := []int{entries[0].VID, entries[1].VID, entries[2].VID}; !reflect.DeepEqual(got, []int{1, 8, 1003}) {
		t.Fatalf("VIDs = %v", got)
	}
	assertPorts(t, entries[0].Untagged, []int{1, 2})
	assertPorts(t, entries[0].Tagged, []int{})
	assertPorts(t, entries[1].Tagged, []int{1})
	assertPorts(t, entries[1].Untagged, []int{5})
	assertPorts(t, entries[2].Tagged, []int{1, 2})
	assertPorts(t, entries[2].Untagged, []int{3, 4})
}

func TestKeepsTheVendorRowIndex(t *testing.T) {
	// Editing and deleting address the table slot, not the VID.
	entries := ParseVLANEntries(realVLANXML)
	got := []int{entries[0].Index, entries[1].Index, entries[2].Index}
	if !reflect.DeepEqual(got, []int{1, 2, 3}) {
		t.Errorf("indexes = %v, want [1 2 3]", got)
	}
}

func TestAgreesWithTheSwitchOwnPVIDs(t *testing.T) {
	// Cross-check: each port's PVID must be a VLAN it is untagged in. This is
	// what proves the bit-shift is right rather than merely self-consistent —
	// the two values come from different endpoints.
	untaggedVLAN := map[int]int{}
	for _, entry := range ParseVLANEntries(realVLANXML) {
		for _, port := range entry.Untagged {
			untaggedVLAN[port] = entry.VID
		}
	}
	for index, want := range capturedPVIDs {
		if got := untaggedVLAN[index+1]; got != want {
			t.Errorf("port %d is untagged in VLAN %d but its PVID is %d",
				index+1, got, want)
		}
	}
}

func TestParsingIsForgiving(t *testing.T) {
	if got := len(ParseVLANEntries("1,1,1,0,0,6;")); got != 1 {
		t.Errorf("trailing separator: got %d entries", got)
	}
	if got := len(ParseVLANEntries("1,1,1,0,0,6;garbage;2,8,8,0,2,32;")); got != 2 {
		t.Errorf("malformed record: got %d entries", got)
	}
	if got := len(ParseVLANEntries("")); got != 0 {
		t.Errorf("empty payload: got %d entries", got)
	}
}

func TestAPortIsNeverBothTaggedAndUntagged(t *testing.T) {
	// tagMbrs=6 and untagMbrs=6 both claim ports 1-2.
	entry := ParseVLANEntries("1,5,5,0,6,6;")[0]
	assertPorts(t, entry.Tagged, []int{1, 2})
	assertPorts(t, entry.Untagged, []int{})
}

func TestParsesTheListPage(t *testing.T) {
	pvid, portCount, enabled, management := parseListPage(realListPage)
	if portCount != 5 {
		t.Errorf("portCount = %d, want 5", portCount)
	}
	if !enabled {
		t.Error("802.1Q should read as enabled")
	}
	if management != 1 {
		t.Errorf("management VLAN = %d, want 1", management)
	}
	for index, want := range capturedPVIDs {
		if got := pvid[index+1]; got != want {
			t.Errorf("PVID of port %d = %d, want %d", index+1, got, want)
		}
	}
}

func TestVlanStateZeroMeansDisabled(t *testing.T) {
	_, _, enabled, _ := parseListPage("var vlanState = 0;")
	if enabled {
		t.Error("vlanState 0 must read as disabled")
	}
}

// -- safety guard ----------------------------------------------------------

func currentConfig() Config {
	return Config{
		VLANs: []VLANEntry{
			{VID: 1, Untagged: []int{1, 2}, Index: 1},
			{VID: 8, Tagged: []int{1}, Untagged: []int{5}, Index: 2},
		},
		PVID:           map[int]int{1: 1, 2: 1, 5: 8},
		PortCount:      5,
		ManagementVLAN: 1,
	}
}

func TestRefusesRemovingAPortFromTheManagementVLAN(t *testing.T) {
	proposed := []VLANEntry{
		{VID: 1, Untagged: []int{2}, Index: 1}, // port 1 dropped
		{VID: 8, Tagged: []int{1}, Untagged: []int{5}, Index: 2},
	}
	err := guard(currentConfig(), proposed, map[int]int{1: 1, 2: 1, 5: 8}, false)
	if !errors.Is(err, ErrUnsafe) || !strings.Contains(err.Error(), "management") {
		t.Fatalf("want an unsafe-change error about the management VLAN, got %v", err)
	}
}

func TestRefusesDeletingTheManagementVLAN(t *testing.T) {
	proposed := []VLANEntry{{VID: 8, Tagged: []int{1}, Untagged: []int{5}, Index: 2}}
	if err := guard(currentConfig(), proposed, map[int]int{1: 8, 2: 8, 5: 8}, false); !errors.Is(err, ErrUnsafe) {
		t.Fatalf("want ErrUnsafe, got %v", err)
	}
}

func TestForceOverridesTheGuard(t *testing.T) {
	proposed := []VLANEntry{{VID: 8, Tagged: []int{1}, Untagged: []int{5}, Index: 2}}
	if err := guard(currentConfig(), proposed, map[int]int{1: 8}, true); err != nil {
		t.Fatalf("force must not raise, got %v", err)
	}
}

func TestRefusesAPVIDPointingAtAMissingVLAN(t *testing.T) {
	err := guard(currentConfig(), currentConfig().VLANs, map[int]int{1: 1, 2: 999, 5: 8}, false)
	if !errors.Is(err, ErrUnsafe) || !strings.Contains(err.Error(), "PVID") {
		t.Fatalf("want an unsafe-change error about a PVID, got %v", err)
	}
}

func TestAllowsAddingPortsToTheManagementVLAN(t *testing.T) {
	proposed := []VLANEntry{
		{VID: 1, Untagged: []int{1, 2, 3}, Index: 1},
		{VID: 8, Tagged: []int{1}, Untagged: []int{5}, Index: 2},
	}
	if err := guard(currentConfig(), proposed, map[int]int{1: 1, 2: 1, 5: 8}, false); err != nil {
		t.Fatalf("adding ports is safe, got %v", err)
	}
}

func TestNoManagementVLANKnownMeansNoGuard(t *testing.T) {
	// Without a session the management VLAN is unknown; do not invent one.
	current := Config{VLANs: []VLANEntry{{VID: 1, Untagged: []int{1}, Index: 1}}, Partial: true}
	if err := guard(current, nil, nil, false); err != nil {
		t.Fatalf("an unknown management VLAN must not block, got %v", err)
	}
}

// -- table slots -----------------------------------------------------------

func TestFirstFreeIndexSkipsUsedSlots(t *testing.T) {
	vlans := []VLANEntry{{VID: 1, Index: 1}, {VID: 8, Index: 3}}
	if got := firstFreeIndex(vlans); got != 2 {
		t.Errorf("firstFreeIndex = %d, want 2", got)
	}
	if got := firstFreeIndex(nil); got != 1 {
		t.Errorf("firstFreeIndex on an empty table = %d, want 1", got)
	}
}

func TestRejectsAnOutOfRangeVID(t *testing.T) {
	client := testClient(t, nil)
	if _, err := client.WriteVLAN(context.Background(), VLANEntry{VID: 5000, Untagged: []int{1}}, false); err == nil ||
		!strings.Contains(err.Error(), "out of range") {
		t.Fatalf("want an out-of-range error, got %v", err)
	}
}

// -- session ---------------------------------------------------------------

// recorder captures every request a test server received, so the wire format
// can be asserted rather than assumed.
type recorder struct {
	method      string
	path        string
	rawQuery    string
	body        string
	contentType string
}

func testClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	scheme, host := "http", "127.0.0.1:1"
	if handler != nil {
		server := httptest.NewServer(handler)
		t.Cleanup(server.Close)
		parsed, err := url.Parse(server.URL)
		if err != nil {
			t.Fatal(err)
		}
		host = parsed.Host
	}
	client, err := NewClient(host, "hunter2hunter2", scheme, false, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func TestPasswordIsHashedNotSentInClear(t *testing.T) {
	// The firmware hashes in the browser; we must do the same.
	var seen recorder
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		seen = recorder{r.Method, r.URL.Path, r.URL.RawQuery, string(body), r.Header.Get("Content-Type")}
		http.SetCookie(w, &http.Cookie{Name: "Cookies", Value: "TOKEN123="})
		_, _ = io.WriteString(w, "<script>var allow = 1;</script>")
	})

	if err := client.Login(context.Background()); err != nil {
		t.Fatalf("login: %v", err)
	}

	sum := sha256.Sum256([]byte("hunter2hunter2"))
	want := "password=" + hex.EncodeToString(sum[:])
	if seen.body != want {
		t.Errorf("login body = %q, want %q", seen.body, want)
	}
	if strings.Contains(seen.body, "hunter2hunter2") {
		t.Error("the plaintext password must never reach the wire")
	}
}

func TestLogoutUsesTheFirmwareFormat(t *testing.T) {
	// Regression guard for the shape that actually releases the session. A GET
	// answers 200 and frees nothing; an empty POST hangs. Both look like
	// success while leaving the owner locked out of their own switch until the
	// session times out, so the exact format is pinned here.
	var calls []recorder
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		calls = append(calls, recorder{r.Method, r.URL.Path, r.URL.RawQuery, string(body), r.Header.Get("Content-Type")})
		if r.URL.Path == "/logon.cgi" {
			http.SetCookie(w, &http.Cookie{Name: "Cookies", Value: "TOKEN123="})
			_, _ = io.WriteString(w, "<script>var allow = 1;</script>")
		}
	})

	ctx := context.Background()
	if err := client.Login(ctx); err != nil {
		t.Fatalf("login: %v", err)
	}
	if err := client.Logout(ctx); err != nil {
		t.Fatalf("logout: %v", err)
	}

	logout := calls[len(calls)-1]
	if logout.path != "/zlogout.cgi" {
		t.Fatalf("logout hit %s", logout.path)
	}
	if logout.method != http.MethodPost {
		t.Error("logout must be a POST, a GET frees nothing")
	}
	if logout.contentType != "text/plain" {
		t.Errorf("the firmware's form is enctype=text/plain, got %q", logout.contentType)
	}
	if logout.body != "Cookies=TOKEN123=\r\n" {
		t.Errorf("the session token goes in the body, got %q", logout.body)
	}
	if client.loggedIn {
		t.Error("the client must not still believe it holds a session")
	}
}

func TestLogoutIsANoopWhenNotLoggedIn(t *testing.T) {
	calls := 0
	client := testClient(t, func(http.ResponseWriter, *http.Request) { calls++ })
	if err := client.Logout(context.Background()); err != nil {
		t.Fatalf("logout: %v", err)
	}
	if calls != 0 {
		t.Errorf("logout made %d requests when no session was held", calls)
	}
}

func TestBusySessionIsNotReportedAsABadPassword(t *testing.T) {
	// Two very different problems; conflating them sends you hunting for a
	// password that was never wrong.
	client := testClient(t, loginFailure("2"))
	err := client.Login(context.Background())
	if !errors.Is(err, ErrBusy) {
		t.Fatalf("want ErrBusy, got %v", err)
	}
	if errors.Is(err, ErrAuth) {
		t.Error("a busy switch must not be reported as a bad password")
	}
}

func TestRejectedPasswordIsReportedAsSuch(t *testing.T) {
	client := testClient(t, loginFailure("1"))
	if err := client.Login(context.Background()); !errors.Is(err, ErrAuth) {
		t.Fatalf("want ErrAuth, got %v", err)
	}
}

func TestLoginWithoutAPasswordFailsFast(t *testing.T) {
	client := testClient(t, nil)
	client.Password = ""
	err := client.Login(context.Background())
	if !errors.Is(err, ErrAuth) || !strings.Contains(err.Error(), "no web password") {
		t.Fatalf("want a missing-password error, got %v", err)
	}
}

func loginFailure(errType string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/logon.cgi":
			_, _ = io.WriteString(w, "<script>var allow = 0;</script>")
		case "/zlogin.html":
			_, _ = io.WriteString(w, `<script>var errType = "`+errType+`";</script>`)
		}
	}
}

// -- writes over the wire --------------------------------------------------

// switchStub serves the captured pages and records every CGI call, so a write
// can be asserted end to end without hardware.
//
// afterWrite, when set, becomes the VLAN table once zqvlanSet.cgi has been
// called. That is what makes the read-back meaningful: the driver's verify
// step must see the change, and the two-phase table is how a stub reproduces
// a switch that actually applied it.
type switchStub struct {
	vlanXML    string
	afterWrite string
	listPage   string
	written    bool
	calls      []recorder
}

func (s *switchStub) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		s.calls = append(s.calls, recorder{r.Method, r.URL.Path, r.URL.RawQuery, string(body), r.Header.Get("Content-Type")})
		switch r.URL.Path {
		case "/logon.cgi":
			http.SetCookie(w, &http.Cookie{Name: "Cookies", Value: "TOKEN123="})
			_, _ = io.WriteString(w, "<script>var allow = 1;</script>")
		case "/vlanEntry.xml":
			table := s.vlanXML
			if s.written && s.afterWrite != "" {
				table = s.afterWrite
			}
			_, _ = io.WriteString(w, table)
		case "/zVLAN_1Q_List.html":
			_, _ = io.WriteString(w, s.listPage)
		case "/zqvlanSet.cgi":
			s.written = true
		}
	}
}

func (s *switchStub) call(path string) (recorder, bool) {
	for _, c := range s.calls {
		if c.path == path {
			return c, true
		}
	}
	return recorder{}, false
}

func TestCreatingAVLANSendsTheFirmwareParameterOrder(t *testing.T) {
	// The firmware's own JavaScript emits action first and trunkBitMap last.
	// url.Values.Encode() would sort them alphabetically; nothing says this
	// CGI tolerates that, so the order is pinned.
	stub := &switchStub{
		vlanXML: realVLANXML,
		// The switch applied it: VLAN 20 appears in slot 4, port 1 tagged
		// (bitmap 2) and ports 3-4 untagged (bitmap 24).
		afterWrite: realVLANXML + "4,20,20,0,2,24;",
		listPage:   realListPage,
	}
	client := testClient(t, stub.handler())

	_, err := client.WriteVLAN(context.Background(),
		VLANEntry{VID: 20, Tagged: []int{1}, Untagged: []int{3, 4}}, false)
	if err != nil {
		t.Fatalf("WriteVLAN: %v", err)
	}

	set, ok := stub.call("/zqvlanSet.cgi")
	if !ok {
		t.Fatal("the driver never called zqvlanSet.cgi")
	}
	// Port 1 tagged -> 2; ports 3 and 4 untagged -> 24. Slots 1-3 are taken,
	// so the new VLAN lands in slot 4.
	want := "action=add&vid=20&vidx=4&fid=0&untagMbrs=24&tagMbrs=2&name=20&trunkBitMap=0"
	if set.rawQuery != want {
		t.Errorf("zqvlanSet.cgi query\n got %s\nwant %s", set.rawQuery, want)
	}
	if _, ok := stub.call("/zlogout.cgi"); !ok {
		t.Error("a write must always release the session")
	}
}

func TestModifyingAVLANSendsTheMembershipDelta(t *testing.T) {
	// An existing VLAN is addressed by its table slot, and the firmware wants
	// the added and removed ports spelled out alongside the new bitmaps.
	stub := &switchStub{
		// VLAN 1003 currently has ports 1-2 tagged (6) and 3-4 untagged (24).
		vlanXML: realVLANXML,
		// Afterwards: port 1 tagged (2), ports 3-4-5 untagged (56).
		afterWrite: "1,1,1,0,0,6;2,8,8,0,2,32;3,1003,1003,0,2,56;",
		listPage:   realListPage,
	}
	client := testClient(t, stub.handler())

	_, err := client.WriteVLAN(context.Background(),
		VLANEntry{VID: 1003, Tagged: []int{1}, Untagged: []int{3, 4, 5}}, false)
	if err != nil {
		t.Fatalf("WriteVLAN: %v", err)
	}

	set, _ := stub.call("/zqvlanSet.cgi")
	// Slot 3. Port 5 joined (bitmap 32), port 2 left (bitmap 4): two changes.
	want := "action=mod&vid=1003&vidx=3&untagMbrs=56&tagMbrs=2&addPbmp=32&delPbmp=4&changePcnt=2&name=1003&trunkBitMap=0"
	if set.rawQuery != want {
		t.Errorf("zqvlanSet.cgi query\n got %s\nwant %s", set.rawQuery, want)
	}
}

func TestAWriteTheSwitchIgnoredIsReportedAsAFailure(t *testing.T) {
	// These CGI endpoints answer 200 whether or not they accepted the request,
	// so the reply proves nothing: only the read-back does.
	stub := &switchStub{vlanXML: realVLANXML, listPage: realListPage}
	client := testClient(t, stub.handler())

	_, err := client.WriteVLAN(context.Background(),
		VLANEntry{VID: 20, Untagged: []int{3}}, false)
	if err == nil || !strings.Contains(err.Error(), "absent after the write") {
		t.Fatalf("want a verification failure, got %v", err)
	}
}

func TestDeletingAVLANStillUsedAsAPVIDIsRefused(t *testing.T) {
	stub := &switchStub{vlanXML: realVLANXML, listPage: realListPage}
	client := testClient(t, stub.handler())

	_, err := client.DeleteVLAN(context.Background(), 8, false)
	if err == nil || !strings.Contains(err.Error(), "PVID of port(s) [5]") {
		t.Fatalf("want a refusal naming port 5, got %v", err)
	}
	if _, ok := stub.call("/zqvlanSet.cgi"); ok {
		t.Error("nothing should have been sent to the switch")
	}
}

func assertPorts(t *testing.T, got, want []int) {
	t.Helper()
	if len(got) == 0 && len(want) == 0 {
		return
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ports = %v, want %v", got, want)
	}
}
