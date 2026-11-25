package doctor

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/ShizukaJiku/gitsi/internal/procx"
	"github.com/ShizukaJiku/gitsi/internal/winps"
)

// Level is a check's result.
type Level int

const (
	Pass Level = iota
	Info
	Warn
	Fail
)

func (l Level) String() string {
	return [...]string{"OK  ", "INFO", "WARN", "FAIL"}[l]
}

// Result is one line of the diagnosis.
type Result struct {
	Level  Level
	Name   string
	Detail string
}

// Deps gathers everything the diagnosis touches in the system, to test it without a network or real processes. It only uses
// standard system binaries and plain TCP connections: no web requests.
type Deps struct {
	LookPath   func(string) (string, error)
	Output     func(ctx context.Context, name string, args ...string) (string, error)
	Dial       func(addr string, timeout time.Duration) error
	Interfaces func() []string
	FileExists func(string) bool
}

// systemDeps is the seam through which commands get the machine's dependencies; tests replace it.
var systemDeps = SystemDeps

// SystemDeps are the real dependencies.
func SystemDeps() Deps {
	return Deps{
		LookPath: exec.LookPath,
		Output: func(ctx context.Context, name string, args ...string) (string, error) {
			cmd := exec.CommandContext(ctx, name, args...)
			cmd.Env = winps.Env(os.Environ()) // the hub's diagnostics run powershell.exe
			b, err := procx.Background(cmd).CombinedOutput()
			return string(b), err
		},
		Dial: func(addr string, timeout time.Duration) error {
			c, err := net.DialTimeout("tcp", addr, timeout)
			if err != nil {
				return err
			}
			return c.Close()
		},
		Interfaces: func() []string {
			ifs, _ := net.Interfaces()
			var names []string
			for _, i := range ifs {
				if i.Flags&net.FlagUp != 0 {
					names = append(names, i.Name)
				}
			}
			return names
		},
		FileExists: func(p string) bool { _, err := os.Stat(p); return err == nil },
	}
}

// Listener is a listening TCP socket.
type Listener struct {
	Addr string
	Port int
	PID  string
}

// Loopback reports whether it only accepts local connections.
func (l Listener) Loopback() bool {
	ip := net.ParseIP(l.Addr)
	return ip != nil && ip.IsLoopback() // all of 127.x.x.x and ::1, not only 127.0.0.1
}

// ParseListeners extracts the listening sockets from `netstat -ano -p tcp`. It does not depend on the language: a listening socket
// has a remote address with port 0.
func ParseListeners(netstat string) []Listener {
	var out []Listener
	for _, line := range strings.Split(netstat, "\n") {
		f := strings.Fields(line)
		if len(f) < 5 || !strings.EqualFold(f[0], "TCP") || !strings.HasSuffix(f[2], ":0") {
			continue
		}
		i := strings.LastIndex(f[1], ":")
		if i < 0 {
			continue
		}
		port, err := strconv.Atoi(f[1][i+1:])
		if err != nil {
			continue
		}
		addr := strings.Trim(f[1][:i], "[]")
		out = append(out, Listener{Addr: addr, Port: port, PID: f[len(f)-1]})
	}
	return out
}

// VPNAdapters returns the active interfaces that look like VPN ones.
func VPNAdapters(names []string) []string {
	var v []string
	for _, n := range names {
		l := strings.ToLower(n)
		if strings.Contains(l, "anyconnect") || strings.Contains(l, "cisco") || strings.Contains(l, "vpn") {
			v = append(v, n)
		}
	}
	return v
}

// Options are the command's flags.
type Options struct {
	Tunnel bool
	Serve  bool // test `gitsi serve --stdio` on the hub (a real protocol session)
	// Layout discovers the hub's layout (env.HubLayout): the hub announces it.
	Layout LayoutFunc
}

func add(rs *[]Result, l Level, name, format string, a ...any) {
	*rs = append(*rs, Result{l, name, fmt.Sprintf(format, a...)})
}

// toolChecks verifies the binaries gitsi needs.
func toolChecks(ctx context.Context, d Deps, rs *[]Result, tools ...string) {
	for _, t := range tools {
		p, err := d.LookPath(t)
		if err != nil {
			add(rs, Fail, "tool "+t, "not found on the PATH")
			continue
		}
		if (t == "ssh" || t == "scp") && !strings.Contains(strings.ToLower(p), `\system32\openssh\`) {
			add(rs, Warn, "tool "+t, "%s is not Windows' OpenSSH (System32\\OpenSSH); gitsi is tested with that one", p)
			continue
		}
		add(rs, Pass, "tool "+t, "%s", p)
	}
}

// listenerChecks checks that the machine exposes nothing beyond what is expected. extraLoopback: local ports gitsi uses for
// tunnels, which must be on 127.0.0.1 only. sshdPIDs (hub only): PIDs of the sshd process; with them it checks on WHICH port sshd
// listens (setup hub accepts -port) and that no other program holds port 22. Without known PIDs it looks at port 22.
func listenerChecks(ctx context.Context, d Deps, rs *[]Result, extraLoopback []int, hub bool, sshdPIDs ...string) {
	out, err := d.Output(ctx, "netstat", "-ano", "-p", "tcp")
	if err != nil && strings.TrimSpace(out) == "" {
		add(rs, Warn, "listening ports", "could not run netstat: %v", err)
		return
	}
	ls := ParseListeners(out)
	open := map[int]bool{}
	for _, l := range ls {
		if !l.Loopback() {
			open[l.Port] = true
		}
	}
	bad := false
	for _, p := range extraLoopback {
		for _, l := range ls {
			if l.Port == p && !l.Loopback() {
				add(rs, Fail, "local tunnel :"+strconv.Itoa(p), "listens on %s; it must be 127.0.0.1 only", l.Addr)
				bad = true
			}
		}
	}
	if hub {
		if len(sshdPIDs) > 0 {
			var ports []string
			for _, l := range ls {
				for _, pid := range sshdPIDs {
					if !l.Loopback() && l.PID == pid {
						ports = append(ports, strconv.Itoa(l.Port))
						break
					}
				}
			}
			if len(ports) > 0 {
				add(rs, Pass, "sshd", "listening on port %s", strings.Join(ports, ", "))
			} else {
				add(rs, Fail, "sshd", "the service runs but its process does not listen outside loopback (check Port in sshd_config and the firewall)")
			}
		} else if open[22] {
			add(rs, Pass, "sshd", "listening on port 22")
		} else {
			add(rs, Fail, "sshd", "not listening on port 22 (is the service stopped?)")
		}
		add(rs, Info, "exposed ports", "%d ports listen outside loopback (Windows' own and the services you start are expected)", len(open))
		return
	}
	if !bad {
		add(rs, Pass, "edge client only", "no gitsi port listens outside 127.0.0.1")
	}
	add(rs, Info, "Windows ports", "%d ports listen outside loopback (the system's own; gitsi opens none)", len(open))
}

func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}
