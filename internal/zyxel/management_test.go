package zyxel

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// Fixtures trimmed from the pages 192.168.2.6 served on V1.00(ACPS.2)C0.
const (
	realManagementPage = `<script>
var eeeinfo_ds = { portNum:5, enable_bit:0x0,};
var led_eco = 0;
ip_ds={state:0,vlan:1,maxVlan:4094,autodnsstate:0,ipStr:['192.168.2.6'],netmaskStr:['255.255.255.0'],gatewayStr:['192.168.2.1'],dnsStr:[''],mgmt_vlan:['1'],}
snmp_info={snmpv1:1,snmpv2:1,trapv1:0,trapv2:0,readCm:["public"],writeCm:["private"],}
</script>`

	realIGMPPage = `<script>
igmp_ds = {state:1,fastleaveState:1,suppressionState:0, umcdropState:0, vids:[1,8,1003,27,],vlan_count:4,rstic:0,rdyna:0,vlanState:2,count:128}
</script>`

	realAdvancedPage = `<script>
var port_iso = 0;
portspeed_info={ingress:[0,0,0,0,0,], egress:[0,0,0,0,0,]}
</script>`
)

func TestParsesTheRealManagementPages(t *testing.T) {
	var management Management
	if err := parseManagementPage(realManagementPage, &management); err != nil {
		t.Fatalf("parseManagementPage: %v", err)
	}
	if management.EEE {
		t.Error("EEE reads as on; enable_bit is 0x0")
	}
	if management.LED {
		t.Error("LED reads as on; led_eco is 0")
	}
	if management.ManagementVLAN != 1 {
		t.Errorf("management VLAN = %d, want 1", management.ManagementVLAN)
	}
	if !management.SNMPEnabled {
		t.Error("SNMP reads as off; snmpv2 is 1 — and Switchboard polls these switches")
	}

	if err := parseIGMPPage(realIGMPPage, &management); err != nil {
		t.Fatalf("parseIGMPPage: %v", err)
	}
	if !management.IGMPSnooping {
		t.Error("IGMP snooping reads as off; state is 1")
	}
	if management.IGMPUnknownDrop {
		t.Error("unknown-multicast drop reads as on; umcdropState is 0")
	}
	if management.IGMPStaticRouterPort != 0 {
		t.Errorf("static router port = %d, want 0 (automatic)", management.IGMPStaticRouterPort)
	}

	parseAdvancedPage(realAdvancedPage, &management)
	if len(management.IngressKbps) != 5 || len(management.EgressKbps) != 5 {
		t.Fatalf("got %d ingress and %d egress figures, want 5 each",
			len(management.IngressKbps), len(management.EgressKbps))
	}
	for i := range management.IngressKbps {
		if management.IngressKbps[i] != 0 || management.EgressKbps[i] != 0 {
			t.Errorf("port %d is capped; the switch reports no limit anywhere", i+1)
		}
	}
	if management.PortIsolationUplink != 0 {
		t.Errorf("port isolation = %d, want 0", management.PortIsolationUplink)
	}
}

func TestAnUnknownManagementPageIsReportedNotGuessed(t *testing.T) {
	var management Management
	if err := parseManagementPage("<html>autre chose</html>", &management); err == nil ||
		!strings.Contains(err.Error(), "eeeinfo_ds") {
		t.Fatalf("want an explicit parse failure, got %v", err)
	}
}

func TestManagementRoundTrip(t *testing.T) {
	device := newSeededFake(t)
	client := clientForFake(t, device, "s3cret")
	ctx := context.Background()

	before, err := client.ReadManagement(ctx)
	if err != nil {
		t.Fatalf("ReadManagement: %v", err)
	}
	if !before.SNMPEnabled || !before.IGMPSnooping {
		t.Fatalf("seed unexpected: %+v", before)
	}

	want := before
	want.EEE = true
	want.LED = true
	want.IGMPUnknownDrop = true
	want.IGMPStaticRouterPort = 3
	want.PortIsolationUplink = 1

	after, err := client.WriteManagement(ctx, want, false)
	if err != nil {
		t.Fatalf("WriteManagement: %v", err)
	}
	if !after.EEE || !after.LED || !after.IGMPUnknownDrop {
		t.Errorf("réglages non appliqués : %+v", after)
	}
	if after.IGMPStaticRouterPort != 3 {
		t.Errorf("static router port = %d, want 3", after.IGMPStaticRouterPort)
	}
	if after.PortIsolationUplink != 1 {
		t.Errorf("port isolation = %d, want 1", after.PortIsolationUplink)
	}
	// Ce qu'on n'a pas demandé ne doit pas avoir bougé.
	if !after.SNMPEnabled || !after.IGMPSnooping {
		t.Errorf("un réglage non demandé a changé : %+v", after)
	}
}

// TestRefusesToBlindSwitchboard guards a switch whose port counters something
// else depends on: turning SNMP off is silent, and the thing that stops
// working is a dashboard nobody is looking at when the apply runs.
func TestRefusesToBlindSwitchboard(t *testing.T) {
	device := newSeededFake(t)
	client := clientForFake(t, device, "s3cret")
	ctx := context.Background()

	current, err := client.ReadManagement(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := current
	want.SNMPEnabled = false

	if _, err := client.WriteManagement(ctx, want, false); !errors.Is(err, ErrUnsafe) {
		t.Fatalf("want ErrUnsafe, got %v", err)
	}
	if _, err := client.WriteManagement(ctx, want, true); err != nil {
		t.Fatalf("force must lift the refusal, got %v", err)
	}
}

func TestPortRatesLeaveOtherPortsAlone(t *testing.T) {
	device := newSeededFake(t)
	client := clientForFake(t, device, "s3cret")
	ctx := context.Background()

	after, err := client.WritePortRates(ctx, 3, 10240, 2048)
	if err != nil {
		t.Fatalf("WritePortRates: %v", err)
	}
	if after.IngressKbps[2] != 10240 || after.EgressKbps[2] != 2048 {
		t.Errorf("port 3 = %d/%d kbps", after.IngressKbps[2], after.EgressKbps[2])
	}
	for i, rate := range after.IngressKbps {
		if i != 2 && rate != 0 {
			t.Errorf("le port %d a été plafonné sans qu'on le demande : %d", i+1, rate)
		}
	}
}

// TestRatesOutsideTheFirmwareGridAreRefused keeps the driver from silently
// rounding: the firmware stores rates in steps of 32 kbps, so a figure it
// cannot represent has to be reported rather than quietly changed.
func TestRatesOutsideTheFirmwareGridAreRefused(t *testing.T) {
	device := newSeededFake(t)
	client := clientForFake(t, device, "s3cret")
	ctx := context.Background()

	for _, rate := range []int{16, 100, 2_000_000} {
		if _, err := client.WritePortRates(ctx, 2, rate, 0); err == nil {
			t.Errorf("le débit %d aurait dû être refusé", rate)
		}
	}
	if _, err := client.WritePortRates(ctx, 2, 0, 0); err != nil {
		t.Errorf("zéro doit lever le plafond, obtenu %v", err)
	}
}
