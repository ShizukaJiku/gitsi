package doctor

import (
	"context"
	"strings"
	"testing"
)

// Output of `ipconfig /all` on a Spanish-language Windows with a VPN client (adapter "Ethernet 2", a generic name): the parser
// must not depend on the system language.
const ipconfigES = `
Configuración IP de Windows

   Nombre de host. . . . . . . . . . . . . . : WORKSTATION

Adaptador de Ethernet Ethernet 2:

   Sufijo DNS específico para la conexión. . : corp.example
   Descripción . . . . . . . . . . . . . . . : Example VPN Virtual Adapter for Windows x64
   Dirección física. . . . . . . . . . . . . : 00-00-5E-00-53-01
   Dirección IPv4. . . . . . . . . . . . . . : 10.20.30.40(Preferido)
   Máscara de subred . . . . . . . . . . . . : 255.255.255.128
   Puerta de enlace predeterminada . . . . . :

Adaptador de Ethernet Ethernet:

   Estado de los medios. . . . . . . . . . . : medios desconectados
   Descripción . . . . . . . . . . . . . . . : Realtek PCIe GbE Family Controller

Adaptador de LAN inalámbrica Wi-Fi:

   Sufijo DNS específico para la conexión. . :
   Descripción . . . . . . . . . . . . . . . : Intel(R) Wi-Fi 6 AX201 160MHz
   Dirección IPv4. . . . . . . . . . . . . . : 192.168.1.7(Preferido)
   Puerta de enlace predeterminada . . . . . : 192.168.1.1
`

func TestParseIPConfigAllDetectsVPNByDescription(t *testing.T) {
	ads := ParseIPConfigAll(ipconfigES)
	if len(ads) != 3 {
		t.Fatalf("expected 3 adapters: %+v", ads)
	}
	vpn, eth, wifi := ads[0], ads[1], ads[2]
	if vpn.Name != "Ethernet 2" || !vpn.Up || !vpn.IsVPN() || vpn.IPv4 != "10.20.30.40" || vpn.HasGW || vpn.DNSSuffix != "corp.example" {
		t.Fatalf("vpn: %+v", vpn)
	}
	if eth.Up || eth.IsVPN() {
		t.Errorf("the disconnected cable does not count: %+v", eth)
	}
	if !wifi.Up || wifi.IsVPN() || wifi.IPv4 != "192.168.1.7" || !wifi.HasGW {
		t.Errorf("wifi: %+v", wifi)
	}
}

func TestParseIPConfigAllEnglish(t *testing.T) {
	out := "Ethernet adapter Ethernet 3:\r\n\r\n   Description . . . . . . . . . . . : WireGuard Tunnel\r\n   IPv4 Address. . . . . . . . . . . : 10.1.2.3(Preferred)\r\n   Default Gateway . . . . . . . . . : 10.1.2.1\r\n"
	ads := ParseIPConfigAll(out)
	if len(ads) != 1 || !ads[0].IsVPN() || ads[0].IPv4 != "10.1.2.3" || !ads[0].HasGW {
		t.Fatalf("%+v", ads)
	}
}

func TestVPNCheckReportsSplitTunnel(t *testing.T) {
	d := deps(nil, "")
	d.Output = func(_ context.Context, name string, _ ...string) (string, error) { return ipconfigES, nil }
	var rs []Result
	vpnCheck(context.Background(), d, &rs)
	if len(rs) != 1 || rs[0].Name != "active VPN" || !strings.Contains(rs[0].Detail, "split tunnel") || !strings.Contains(rs[0].Detail, "Ethernet 2") {
		t.Fatalf("%+v", rs)
	}
}

func TestVPNCheckWarnsFullTunnelAndNoVPN(t *testing.T) {
	full := strings.Replace(ipconfigES, "Puerta de enlace predeterminada . . . . . :\r\n", "", 1)
	full = strings.Replace(full, "Máscara de subred . . . . . . . . . . . . : 255.255.255.128\n   Puerta de enlace predeterminada . . . . . :", "Máscara de subred . . . . . . . . . . . . : 255.255.255.128\n   Puerta de enlace predeterminada . . . . . : 10.20.30.1", 1)
	d := deps(nil, "")
	d.Output = func(context.Context, string, ...string) (string, error) { return full, nil }
	var rs []Result
	vpnCheck(context.Background(), d, &rs)
	if !strings.Contains(rs[0].Detail, "ALL traffic") {
		t.Fatalf("with its own gateway it must warn: %+v", rs)
	}

	d.Output = func(context.Context, string, ...string) (string, error) {
		return "Adaptador de LAN inalámbrica Wi-Fi:\n   Descripción . . . : Intel Wi-Fi\n   Dirección IPv4. . : 192.168.1.7\n", nil
	}
	rs = nil
	vpnCheck(context.Background(), d, &rs)
	if rs[0].Name != "VPN" || !strings.Contains(rs[0].Detail, "no active VPN") {
		t.Fatalf("%+v", rs)
	}
}
