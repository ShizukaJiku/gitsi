package provision

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/ShizukaJiku/gitsi/internal/config"
	"github.com/ShizukaJiku/gitsi/internal/install"
	"github.com/ShizukaJiku/gitsi/internal/layout"
	"github.com/ShizukaJiku/gitsi/internal/module"
	"github.com/ShizukaJiku/gitsi/internal/procx"
	"github.com/ShizukaJiku/gitsi/internal/safe"
	"github.com/ShizukaJiku/gitsi/internal/winps"
)

var (
	pubKeyRe  = regexp.MustCompile(`^(ssh-ed25519|ssh-rsa|ecdsa-sha2-\S+)\s+\S+`)
	privateRe = regexp.MustCompile(`PRIVATE KEY`)
)

// System abstracts what setup hub does to the machine, to test it and to offer -dry-run.
type System interface {
	PowerShell(ctx context.Context, script string) (string, error)
	Run(ctx context.Context, name string, args ...string) (string, error)
	ReadFile(path string) ([]byte, error)
	WriteFile(path string, data []byte) error
	Logf(format string, a ...any)
}

// HubOptions are setup hub's data.
type HubOptions struct {
	EdgeIP      string // edge IP (or CIDR): the only one with firewall access
	PublicKey   string // the edge's public key
	User        string // hub user allowed to log in
	Port        int
	UserProfile string // User's profile folder (where ~/.ssh goes if not an administrator); empty: C:\Users\<User>
	ProgramData string // C:\ProgramData
	SSHDBinary  string // path of sshd.exe
}

// Validate checks the inputs, which end up inside PowerShell scripts.
func (o HubOptions) Validate() error {
	if !validEdgeAddr(o.EdgeIP) {
		return fmt.Errorf("-edge-ip must be an IP or a CIDR range (e.g. 192.168.1.7 or 192.168.1.0/24): %q", o.EdgeIP)
	}
	k := strings.TrimSpace(o.PublicKey)
	if privateRe.MatchString(k) {
		return errors.New("that looks like a PRIVATE key. Pass only the public key (.pub); never share the private one")
	}
	if !pubKeyRe.MatchString(k) || strings.ContainsAny(k, "\r\n'\"`$") {
		return errors.New("invalid public key (it must be a single line 'ssh-ed25519 AAAA... comment')")
	}
	if err := safe.Name("user", o.User); err != nil {
		return err
	}
	if o.Port < 1 || o.Port > 65535 {
		return fmt.Errorf("invalid port: %d", o.Port)
	}
	return nil
}

// validEdgeAddr accepts a real IPv4 or IPv4 CIDR range (not just IP-shaped): a 999.1.1.1 would reach the firewall step after
// sshd_config was already changed and sshd stopped.
func validEdgeAddr(s string) bool {
	if strings.Contains(s, "/") {
		ip, _, err := net.ParseCIDR(s)
		return err == nil && ip.To4() != nil && !strings.Contains(s, ":")
	}
	ip := net.ParseIP(s)
	return ip != nil && ip.To4() != nil && !strings.Contains(s, ":")
}

// SetupHub provisions this machine as the hub, step by step. It is idempotent.
func SetupHub(ctx context.Context, sys System, o HubOptions) error {
	if err := o.Validate(); err != nil {
		return err
	}
	cfgPath := filepath.Join(o.ProgramData, "ssh", "sshd_config")
	bakPath := cfgPath + ".gitsi.bak"

	// 1. OpenSSH Server
	sys.Logf("Installing/verifying OpenSSH Server...")
	if _, err := sys.PowerShell(ctx, `$cap = Get-WindowsCapability -Online -Name 'OpenSSH.Server*' | Select-Object -First 1
if ($cap.State -ne 'Installed') { Add-WindowsCapability -Online -Name $cap.Name | Out-Null }
Set-Service -Name sshd -StartupType Automatic
if ((Get-Service sshd).Status -ne 'Running') { Start-Service sshd }
Stop-Service sshd`); err != nil {
		return fmt.Errorf("installing OpenSSH Server: %w", err)
	}

	// 2. hardened sshd_config (with a backup of the original)
	sys.Logf("Hardening sshd_config (key only, only %s, local forwarding only)...", o.User)
	orig, err := sys.ReadFile(cfgPath)
	if err != nil {
		return fmt.Errorf("could not read %s: %w", cfgPath, err)
	}
	if _, err := sys.ReadFile(bakPath); err != nil { // only the first backup: it keeps the factory original
		if err := sys.WriteFile(bakPath, orig); err != nil {
			return err
		}
	}
	hardened := HardenSSHD(strings.Split(strings.ReplaceAll(string(orig), "\r\n", "\n"), "\n"), o.User, o.Port)
	if err := sys.WriteFile(cfgPath, []byte(strings.Join(hardened, "\n")+"\n")); err != nil {
		return err
	}

	// 3. PowerShell as SSH's default shell (with cmd.exe Git fails on the single quotes)
	sys.Logf("Setting PowerShell as SSH's default shell...")
	if _, err := sys.PowerShell(ctx, `New-Item -Path 'HKLM:\SOFTWARE\OpenSSH' -Force | Out-Null
New-ItemProperty -Path 'HKLM:\SOFTWARE\OpenSSH' -Name DefaultShell -Value "$env:SystemRoot\System32\WindowsPowerShell\v1.0\powershell.exe" -PropertyType String -Force | Out-Null`); err != nil {
		return err
	}

	// 4. Public key. By SID, because the group's name is localized ("Administradores" on a Spanish Windows).
	sys.Logf("Registering the edge's public key...")
	out, err := sys.PowerShell(ctx, fmt.Sprintf(`(Get-LocalGroupMember -SID 'S-1-5-32-544' | Where-Object { $_.Name -like '*\%s' }) -ne $null`, o.User))
	if err != nil {
		return fmt.Errorf("could not read the Administrators group: %w", err)
	}
	isAdmin := strings.EqualFold(strings.TrimSpace(out), "True")
	akPath := AuthorizedKeysPath(isAdmin, o.ProgramData, o.User)
	if !isAdmin && o.UserProfile != "" { // the profile folder is not always named after the user
		akPath = filepath.Join(o.UserProfile, ".ssh", "authorized_keys")
	}
	var existing []string
	if b, err := sys.ReadFile(akPath); err == nil {
		existing = strings.Split(strings.ReplaceAll(string(b), "\r\n", "\n"), "\n")
	}
	lines, added := AddKeyLine(existing, o.PublicKey)
	if added || existing == nil {
		if err := sys.WriteFile(akPath, []byte(strings.Join(lines, "\n")+"\n")); err != nil {
			return err
		}
	}
	grant := []string{akPath, "/inheritance:r", "/grant", "SYSTEM:F"}
	if isAdmin {
		grant = append(grant, "/grant", "*S-1-5-32-544:F")
	} else {
		grant = append(grant, "/grant", o.User+":F")
	}
	if _, err := sys.Run(ctx, "icacls", grant...); err != nil {
		return fmt.Errorf("permissions of %s: %w", akPath, err)
	}
	sys.Logf("  administrator user: %v  file: %s  new key: %v", isAdmin, akPath, added)

	// 5. Firewall: only the edge can reach the SSH port
	sys.Logf("Firewall: port %d only from %s...", o.Port, o.EdgeIP)
	if _, err := sys.PowerShell(ctx, fmt.Sprintf(`Get-NetFirewallRule -Name 'gitsi-ssh','OpenSSH-Server-In-TCP' -ErrorAction SilentlyContinue | Remove-NetFirewallRule
New-NetFirewallRule -Name 'gitsi-ssh' -DisplayName 'gitsi SSH (edge only)' -Direction Inbound -Protocol TCP -LocalPort %d -Action Allow -RemoteAddress '%s' -Profile Any | Out-Null`, o.Port, o.EdgeIP)); err != nil {
		return err
	}

	// 6. Validate and start (if the config is invalid, the backup is restored)
	sys.Logf("Validating and starting sshd...")
	if _, err := sys.Run(ctx, o.SSHDBinary, "-t"); err != nil {
		// It goes back to the configuration there was when THIS run started (the working one), not to the factory backup: on a
		// second run the backup is the original, unhardened sshd_config.
		_ = sys.WriteFile(cfgPath, orig)
		return fmt.Errorf("invalid sshd_config, the previous configuration was restored (sshd stays stopped until you fix it): %w", err)
	}
	if _, err := sys.PowerShell(ctx, `Start-Service sshd`); err != nil {
		return err
	}
	return nil
}

// ---- System implementations ----

type realSystem struct{ out io.Writer }

func (s realSystem) PowerShell(ctx context.Context, script string) (string, error) {
	return s.Run(ctx, "powershell.exe", "-NoProfile", "-NonInteractive", "-Command", script)
}

func (s realSystem) Run(ctx context.Context, name string, args ...string) (string, error) {
	var buf bytes.Buffer
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = winps.Env(os.Environ()) // setup hub runs powershell.exe, often from a PowerShell 7 console
	cmd.Stdout, cmd.Stderr = &buf, &buf
	err := procx.Background(cmd).Run()
	if err != nil {
		return buf.String(), fmt.Errorf("%s: %w: %s", name, err, strings.TrimSpace(buf.String()))
	}
	return buf.String(), nil
}

func (s realSystem) ReadFile(p string) ([]byte, error) { return os.ReadFile(p) }
func (s realSystem) WriteFile(p string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	return os.WriteFile(p, data, 0o644)
}
func (s realSystem) Logf(f string, a ...any) { fmt.Fprintf(s.out, f+"\n", a...) }

// dryRun reads real files but neither writes nor runs anything: it only shows what it would do.
type dryRun struct {
	realSystem
	sshdConfig  string // sshd_config: when it does not exist yet (OpenSSH Server not installed), it reads instead...
	sshdDefault string // ...sshd_config_default, the one OpenSSH copies when sshd starts for the first time
}

// defaultSSHDConfig is the effective content of Win32-OpenSSH's sshd_config_default, for when not even that file exists yet.
const defaultSSHDConfig = `AuthorizedKeysFile	.ssh/authorized_keys
Subsystem	sftp	sftp-server.exe
Match Group administrators
       AuthorizedKeysFile __PROGRAMDATA__/ssh/administrators_authorized_keys
`

// ReadFile: in real mode step 1 installs OpenSSH Server and starts sshd, which creates sshd_config. In -dry-run that step does not
// run, so on a machine without OpenSSH Server the file does not exist: the plan is computed on the one that would be created.
func (d dryRun) ReadFile(p string) ([]byte, error) {
	b, err := d.realSystem.ReadFile(p)
	if err == nil || d.sshdConfig == "" || p != d.sshdConfig || !errors.Is(err, fs.ErrNotExist) {
		return b, err
	}
	if b, derr := d.realSystem.ReadFile(d.sshdDefault); derr == nil {
		fmt.Fprintf(d.out, "  [dry-run] %s does not exist yet (OpenSSH Server not installed): the plan starts from %s\n", p, d.sshdDefault)
		return b, nil
	}
	fmt.Fprintf(d.out, "  [dry-run] %s does not exist yet (OpenSSH Server not installed): the plan starts from OpenSSH's factory configuration\n", p)
	return []byte(defaultSSHDConfig), nil
}

func (d dryRun) PowerShell(_ context.Context, script string) (string, error) {
	fmt.Fprintf(d.out, "  [dry-run] PowerShell:\n    %s\n", strings.ReplaceAll(script, "\n", "\n    "))
	return "", nil
}
func (d dryRun) Run(_ context.Context, name string, args ...string) (string, error) {
	fmt.Fprintf(d.out, "  [dry-run] run: %s %s\n", name, strings.Join(args, " "))
	return "", nil
}
func (d dryRun) WriteFile(p string, data []byte) error {
	fmt.Fprintf(d.out, "  [dry-run] write %s (%d bytes)\n", p, len(data))
	return nil
}

// setupHubCmd is the `gitsi setup hub` command.
func setupHubCmd(ctx context.Context, env *module.Env, args []string) error {
	fs := module.NewFlagSet("setup hub", env.Stderr)
	edgeIP := fs.String("edge-ip", "", "edge IP (or CIDR), the only one with firewall access")
	pub := fs.String("public-key", "", "the edge's public key (line 'ssh-ed25519 ...')")
	pubFile := fs.String("public-key-file", "", ".pub file with the edge's public key")
	user := fs.String("user", os.Getenv("USERNAME"), "user of this machine allowed to log in over SSH")
	port := fs.Int("port", 22, "SSH port")
	homeDir := fs.String("home", "", "gitsi's root on this hub: binary, mailbox, jobs (default <home folder>/gitsi)")
	reposDir := fs.String("repos-dir", "", "folder of the bridge repos (default <home>/repos)")
	dry := fs.Bool("dry-run", false, "show what it would do without changing anything")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *pubFile != "" {
		b, err := os.ReadFile(*pubFile)
		if err != nil {
			return err
		}
		for _, l := range strings.Split(string(b), "\n") {
			if strings.TrimSpace(l) != "" {
				*pub = strings.TrimSpace(l)
				break
			}
		}
	}
	opts := HubOptions{
		EdgeIP: *edgeIP, PublicKey: *pub, User: *user, Port: *port,
		ProgramData: os.Getenv("ProgramData"),
		SSHDBinary:  filepath.Join(os.Getenv("SystemRoot"), "System32", "OpenSSH", "sshd.exe"),
	}
	if opts.ProgramData == "" {
		opts.ProgramData = `C:\ProgramData`
	}
	// When the allowed user is the one running this, their real profile is the one Windows already knows (not guessed from the name).
	if strings.EqualFold(opts.User, os.Getenv("USERNAME")) {
		if h, err := os.UserHomeDir(); err == nil {
			opts.UserProfile = h
		}
	}
	if err := opts.Validate(); err != nil {
		return fmt.Errorf("%w\nusage: gitsi setup hub -edge-ip <ip> -public-key \"ssh-ed25519 ...\" [-user <user>] [-dry-run]", err)
	}

	var sys System = realSystem{out: env.Stdout}
	if *dry {
		fmt.Fprintln(env.Stdout, "-dry-run mode: nothing is changed. (The key step assumes a non-administrator user.)")
		sys = dryRun{
			realSystem:  realSystem{out: env.Stdout},
			sshdConfig:  filepath.Join(opts.ProgramData, "ssh", "sshd_config"),
			sshdDefault: filepath.Join(filepath.Dir(opts.SSHDBinary), "sshd_config_default"),
		}
	} else if !IsAdmin() {
		return errors.New("setup hub needs administrator rights: open PowerShell as Administrator (or use -dry-run to see the plan)")
	}

	if err := SetupHub(ctx, sys, opts); err != nil {
		return err
	}
	if *dry {
		return nil
	}

	// Hub role and layout: THIS machine decides it (defaults unless given) and announces it to the edges.
	if *homeDir != "" {
		env.Config.Home = *homeDir
	}
	if *reposDir != "" {
		env.Config.ReposDir = *reposDir
	}
	env.Config.Role = config.RoleHub
	userHome, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	lay := layout.ForHub(env.Config, userHome)
	if err := EnsureHubDirs(lay); err != nil {
		return err
	}
	if err := env.Config.Save(); err != nil {
		return err
	}
	// The hub publishes itself in its Home: without this step the edge could neither start `serve` nor download updates.
	published := ""
	if self, serr := os.Executable(); serr == nil {
		if sum, perr := install.PublishSelf(self, filepath.FromSlash(lay.Binary)); perr == nil {
			published = sum
		} else {
			fmt.Fprintf(env.Stdout, "Warning: could not publish gitsi in %s: %v (run 'gitsi update -publish')\n", lay.Binary, perr)
		}
	}

	ips, _ := sys.PowerShell(ctx, `Get-NetIPAddress -AddressFamily IPv4 | Where-Object { $_.IPAddress -notmatch '^(127\.|169\.254\.)' } | ForEach-Object { $_.IPAddress }`)
	fmt.Fprintf(env.Stdout, "\nDone. This machine is now a hub.\n  Allowed user: %s\n  Port: %d (only from %s)\n  This machine's IP(s): %s\n",
		opts.User, opts.Port, opts.EdgeIP, strings.Join(strings.Fields(ips), ", "))
	fmt.Fprintln(env.Stdout, "Layout (decided by this hub and announced to the edges):")
	printLayout(env.Stdout, lay)
	if published != "" {
		fmt.Fprintln(env.Stdout, "  gitsi published in that folder (SHA256 "+published[:12]+"): the edges can now connect and update")
	}
	fmt.Fprintln(env.Stdout, "Recommended: pin this IP in the router (DHCP reservation) so it does not change.")

	// Pairing ends on the edge: the exact command is printed, with the hub's fingerprint for it to verify and pin.
	hubIP := pickIP(strings.Fields(ips), opts.EdgeIP)
	fp, _ := hubHostFingerprint(sys, opts.ProgramData) // when it cannot be read, the command is printed without a fingerprint
	fmt.Fprint(env.Stdout, pairingGuidance(fp, hubIP, opts.User))
	return nil
}
