package doctor

import (
	"context"
	"regexp"
	"strings"
)

var adapterPrefix = regexp.MustCompile(`(?i)^(adaptador de ethernet|adaptador de lan inal\S+|adaptador desconocido|ethernet adapter|wireless lan adapter|unknown adapter)\s+`)

// Adapter is a network adapter as `ipconfig /all` reports it.
type Adapter struct {
	Name      string // "Ethernet 2"
	Desc      string // e.g. "WireGuard Tunnel" or another vendor's virtual adapter
	Up        bool
	IPv4      string
	HasGW     bool // has a default gateway
	DNSSuffix string
}

// vpnWords identify VPN adapters by their description (the name on Windows is usually generic: "Ethernet 2").
var vpnWords = []string{"anyconnect", "cisco", "vpn", "wireguard", "openvpn", "tap-windows", "wintun", "globalprotect",
	"forticlient", "fortinet", "pulse secure", "juniper", "nordlynx", "zerotier"}

// IsVPN reports whether the adapter looks like a VPN one, by name or description.
func (a Adapter) IsVPN() bool {
	s := strings.ToLower(a.Name + " " + a.Desc)
	for _, w := range vpnWords {
		if strings.Contains(s, w) {
			return true
		}
	}
	return false
}

// key/value of an `ipconfig /all` line: "   Description. . . . : value" (or its localized form).
func splitKV(line string) (key, val string, ok bool) {
	i := strings.Index(line, " . ")
	j := strings.Index(line, ": ")
	if j < 0 && strings.HasSuffix(strings.TrimSpace(line), ":") {
		j = strings.LastIndex(line, ":")
	}
	if j < 0 {
		return "", "", false
	}
	if i < 0 || i > j {
		i = j
	}
	key = strings.ToLower(strings.TrimSpace(line[:i]))
	if j+1 <= len(line) {
		val = strings.TrimSpace(line[j+1:])
	}
	return key, val, true
}

// ParseIPConfigAll interprets `ipconfig /all` output without depending on Windows' language: an adapter starts on an unindented
// line ending in ':'; fields are recognized by their English or Spanish prefixes.
func ParseIPConfigAll(out string) []Adapter {
	var res []Adapter
	var cur *Adapter
	flush := func() {
		if cur != nil {
			res = append(res, *cur)
		}
	}
	for _, raw := range strings.Split(strings.ReplaceAll(out, "\r", ""), "\n") {
		if strings.TrimSpace(raw) == "" {
			continue
		}
		if raw[0] != ' ' && raw[0] != '\t' {
			flush()
			cur = nil
			if t := strings.TrimSpace(raw); strings.HasSuffix(t, ":") {
				name := strings.TrimSuffix(t, ":")
				// "Ethernet adapter Ethernet 2" / "Adaptador de Ethernet Ethernet 2": the prefix is removed (tolerates the OEM encoding)
				name = adapterPrefix.ReplaceAllString(name, "")
				cur = &Adapter{Name: name, Up: true}
			}
			continue
		}
		if cur == nil {
			continue
		}
		k, v, ok := splitKV(raw)
		if !ok {
			continue
		}
		switch {
		case strings.HasPrefix(k, "estado de los medios") || strings.HasPrefix(k, "media state"):
			if strings.Contains(strings.ToLower(v), "desconect") || strings.Contains(strings.ToLower(v), "disconnect") {
				cur.Up = false
			}
		case strings.HasPrefix(k, "descrip"):
			cur.Desc = v
		case strings.HasPrefix(k, "sufijo dns") || strings.HasPrefix(k, "connection-specific dns"):
			cur.DNSSuffix = v
		case strings.Contains(k, "ipv4"):
			v = strings.TrimSpace(strings.NewReplacer("(Preferido)", "", "(Preferred)", "").Replace(v))
			cur.IPv4 = v
		case strings.HasPrefix(k, "puerta de enlace") || strings.HasPrefix(k, "default gateway"):
			if v != "" {
				cur.HasGW = true
			}
		}
	}
	flush()
	return res
}

// systemAdapters reads the adapters with the system binary ipconfig.exe (signed, no PowerShell).
func systemAdapters(ctx context.Context, d Deps) []Adapter {
	out, err := d.Output(ctx, "ipconfig", "/all")
	if err != nil && strings.TrimSpace(out) == "" {
		return nil
	}
	return ParseIPConfigAll(out)
}

// vpnCheck reports whether a VPN is active and how it affects the local network.
func vpnCheck(ctx context.Context, d Deps, rs *[]Result) {
	var vpns []Adapter
	for _, a := range systemAdapters(ctx, d) {
		if a.Up && a.IsVPN() {
			vpns = append(vpns, a)
		}
	}
	if len(vpns) == 0 {
		// fallback by name (when ipconfig was not available)
		if names := VPNAdapters(d.Interfaces()); len(names) > 0 {
			add(rs, Info, "VPN", "active adapter: %s", strings.Join(names, ", "))
			return
		}
		add(rs, Info, "VPN", "no active VPN detected (searched by adapter description in 'ipconfig /all')")
		return
	}
	for _, a := range vpns {
		mode := "with its own gateway: the VPN may carry ALL traffic and cut off the local network"
		if !a.HasGW {
			mode = "without a default gateway (split tunnel): the hub's local network stays reachable"
		}
		add(rs, Info, "active VPN", "%s [%s] %s; %s", a.Name, a.Desc, a.IPv4, mode)
	}
}
