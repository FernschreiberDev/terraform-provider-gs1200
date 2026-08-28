package zyxel

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// realPortPage is the shape zPort.html served on 192.168.2.6 running
// V1.00(ACPS.2)C0, trimmed to the parts this driver reads.
const realPortPage = `<script>
var max_port_num=5;
var lpEn = [1];
var sc_en = 0;
var pps = 0;
all_info = {
state:[1,1,1,1,1,],
spd_cfg:[0,0,0,0,0,],
mode_cfg:[0,0,0,0,0,],
fc_cfg:[0,0,0,0,0,],
ability:[31,31,31,31,31,],
trunk_info:[0,0,0,0,0,],
}
</script>`

// realLoginPage is the data_info block the login page volunteers to anyone,
// with no session at all.
const realLoginPage = `<script>var allow = 1;</script>
<script>var data_info = {sysnameStr:["Gaming"],modelStr:["GS1200-5v3"],
macStr:["c4:9a:31:46:eb:23"],ipStr:["192.168.2.6"],netmaskStr:["255.255.255.0"],
gatewayStr:["192.168.2.1"],dnsStr:["----"],firmwareStr:["V1.00(ACPS.2)C0 "],
system_uptime:["652992"], hardwareStr:["AN8858"]};
</script>`

func TestParsesTheRealPortPage(t *testing.T) {
	settings, err := parsePortPage(realPortPage)
	if err != nil {
		t.Fatalf("parsePortPage: %v", err)
	}
	if len(settings.Ports) != 5 {
		t.Fatalf("got %d ports, want 5", len(settings.Ports))
	}
	for _, port := range settings.Ports {
		if !port.Enabled {
			t.Errorf("port %d reads as disabled; the switch reports every port up", port.Port)
		}
		if port.Speed != SpeedAuto {
			t.Errorf("port %d speed = %q, want %q", port.Port, port.Speed, SpeedAuto)
		}
		if port.FlowControl {
			t.Errorf("port %d reads flow control on; the switch has it off everywhere", port.Port)
		}
	}
	if !settings.LoopPrevention {
		t.Error("loop prevention reads as off; lpEn is [1]")
	}
	if settings.StormControl {
		t.Error("storm control reads as on; sc_en is 0")
	}
}

// TestAnUnknownFirmwareIsReportedNotGuessed keeps the failure honest: a page
// without all_info means the firmware differs from the one this was written
// against, and inventing defaults there would write invented settings.
func TestAnUnknownFirmwareIsReportedNotGuessed(t *testing.T) {
	_, err := parsePortPage("<html>something else entirely</html>")
	if err == nil || !strings.Contains(err.Error(), "all_info") {
		t.Fatalf("want an explicit parse failure, got %v", err)
	}
}

func TestParsesTheRealLoginPage(t *testing.T) {
	device := newSeededFake(t)
	client := clientForFake(t, device, "s3cret")

	info, err := client.ReadDeviceInfo(context.Background())
	if err != nil {
		t.Fatalf("ReadDeviceInfo: %v", err)
	}
	if info.Model != "GS1200-5v3" || info.Name != "Gaming" {
		t.Errorf("got name=%q model=%q", info.Name, info.Model)
	}
	if info.MAC != "c4:9a:31:46:eb:23" || info.Gateway != "192.168.2.1" {
		t.Errorf("got mac=%q gateway=%q", info.MAC, info.Gateway)
	}
	if info.UptimeS != 652992 {
		t.Errorf("uptime = %d, want 652992", info.UptimeS)
	}
}

// TestLinkStatusMatchesTheFleet decodes the two groups that were checked
// against SNMP on all ten ports of the rack. The third and fourth groups stay
// undecoded on purpose — a guess in a data source becomes a fact people rely
// on.
func TestLinkStatusMatchesTheFleet(t *testing.T) {
	cases := []struct {
		name    string
		payload string
		want    []LinkStatus
	}{
		{
			name:    "gs1200",
			payload: `<script> var portStatus = "1,1,0,0,1,&3,3,2,1,3,&1,1,1,0,1,&0,0,0,0,0,&";</script>`,
			want: []LinkStatus{
				{1, true, 1000}, {2, true, 1000}, {3, false, 0}, {4, false, 0}, {5, true, 1000},
			},
		},
		{
			name:    "living",
			payload: `<script> var portStatus = "1,0,1,1,1,&3,1,3,3,2,&1,0,1,1,1,&0,0,0,0,0,&";</script>`,
			want: []LinkStatus{
				// Le port 5 négocie à 100 Mb, ce que le SNMP confirme.
				{1, true, 1000}, {2, false, 0}, {3, true, 1000}, {4, true, 1000}, {5, true, 100},
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := parseLinkStatus(tc.payload)
			if len(got) != len(tc.want) {
				t.Fatalf("got %d ports, want %d", len(got), len(tc.want))
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("port %d: got %+v, want %+v", i+1, got[i], tc.want[i])
				}
			}
		})
	}
}

func TestWritePortSettingsLeavesOtherPortsAlone(t *testing.T) {
	device := newSeededFake(t)
	client := clientForFake(t, device, "s3cret")
	ctx := context.Background()

	before, err := client.ReadSettings(ctx)
	if err != nil {
		t.Fatalf("ReadSettings: %v", err)
	}

	// Port 5 is not in the management VLAN on the seeded configuration.
	if _, err := client.WritePortSettings(ctx, PortSettings{
		Port: 5, Enabled: true, Speed: Speed100Full, FlowControl: true,
	}, false); err != nil {
		t.Fatalf("WritePortSettings: %v", err)
	}

	after, err := client.ReadSettings(ctx)
	if err != nil {
		t.Fatalf("ReadSettings: %v", err)
	}
	got, _ := after.Port(5)
	if got.Speed != Speed100Full || !got.FlowControl {
		t.Errorf("port 5 = %+v", got)
	}
	for port := 1; port <= 4; port++ {
		want, _ := before.Port(port)
		now, _ := after.Port(port)
		if now != want {
			t.Errorf("port %d moved: %+v -> %+v", port, want, now)
		}
	}
}

// TestRefusesToShutThePortCarryingManagement is the guard that matters most
// here. Switching off the uplink takes the switch off the network, and getting
// it back means walking to the rack.
func TestRefusesToShutThePortCarryingManagement(t *testing.T) {
	device := newSeededFake(t)
	client := clientForFake(t, device, "s3cret")
	ctx := context.Background()

	// Port 1 is untagged in VLAN 1, the management VLAN.
	_, err := client.WritePortSettings(ctx, PortSettings{
		Port: 1, Enabled: false, Speed: SpeedAuto,
	}, false)
	if !errors.Is(err, ErrUnsafe) {
		t.Fatalf("want ErrUnsafe, got %v", err)
	}

	settings, _ := client.ReadSettings(ctx)
	if port, _ := settings.Port(1); !port.Enabled {
		t.Error("the refused write switched the port off anyway")
	}

	if _, err := client.WritePortSettings(ctx, PortSettings{
		Port: 1, Enabled: false, Speed: SpeedAuto,
	}, true); err != nil {
		t.Fatalf("force must lift the refusal, got %v", err)
	}
}

func TestRejectsAnUnknownSpeed(t *testing.T) {
	device := newSeededFake(t)
	client := clientForFake(t, device, "s3cret")

	_, err := client.WritePortSettings(context.Background(), PortSettings{
		Port: 2, Enabled: true, Speed: "10-gigabit",
	}, false)
	if err == nil || !strings.Contains(err.Error(), "unknown speed") {
		t.Fatalf("want an unknown-speed error, got %v", err)
	}
}

func TestDeviceNameFollowsTheFirmwareRule(t *testing.T) {
	device := newSeededFake(t)
	client := clientForFake(t, device, "s3cret")
	ctx := context.Background()

	// The firmware's own page refuses anything but 1-14 word characters.
	for _, bad := range []string{"", "un nom avec espaces", "beaucoup-trop-long-pour-lui", "accentué"} {
		if err := client.WriteDeviceName(ctx, bad); err == nil {
			t.Errorf("the name %q should have been refused", bad)
		}
	}

	if err := client.WriteDeviceName(ctx, "Salon"); err != nil {
		t.Fatalf("WriteDeviceName: %v", err)
	}
	info, err := client.ReadDeviceInfo(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if info.Name != "Salon" {
		t.Errorf("name = %q, want Salon", info.Name)
	}
}
