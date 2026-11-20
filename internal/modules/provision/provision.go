// Package provision sets the machines up: `setup edge` configures the edge and `setup hub` provisions the hub (OpenSSH, a
// firewall rule limited to the edge, the public key, PowerShell as the default shell, local AllowTcpForwarding). It also holds
// `setup key` and `config`.
package provision

import "github.com/ShizukaJiku/gitsi/internal/module"

type mod struct{}

// New returns the module.
func New() module.Module { return mod{} }

func (mod) Name() string { return "provision" }

func (mod) Commands() []module.Command {
	return []module.Command{
		{Group: "setup", Name: "hub", Summary: "Set this machine up as the hub (as administrator; -dry-run)", Roles: module.Both, Run: setupHubCmd, Flags: []string{"-dry-run", "-edge-ip", "-home", "-port", "-public-key", "-public-key-file", "-repos-dir", "-user"}},
		{Group: "setup", Name: "edge", Summary: "Set this machine up as an edge (-host, -user)", Roles: module.Both, Run: initCmd, Flags: []string{"-alias", "-host", "-hostkey", "-key", "-port", "-user"}},
		{Group: "setup", Name: "key", Summary: "Change the edge's SSH key without losing access", Roles: module.Edge, NeedsHub: true, Run: setupKeyCmd, Flags: []string{"-key", "-old", "-retire-old"}},
		{Name: "config", Summary: "Show or change the configuration (show, get, set, path)", Roles: module.Both, Run: configCmd, Complete: completeConfig},
	}
}
