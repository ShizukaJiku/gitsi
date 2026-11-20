package provision

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"

	"github.com/ShizukaJiku/gitsi/internal/config"
	"github.com/ShizukaJiku/gitsi/internal/fault"
	"github.com/ShizukaJiku/gitsi/internal/hubview"
	"github.com/ShizukaJiku/gitsi/internal/module"
	"github.com/ShizukaJiku/gitsi/internal/safe"
	"github.com/ShizukaJiku/gitsi/internal/transport"
)

// initCmd sets this machine up as an edge: key, ~/.ssh/config, gitsi's config and a connection test.
func initCmd(ctx context.Context, env *module.Env, args []string) error {
	fs := flag.NewFlagSet("setup edge", flag.ContinueOnError)
	fs.SetOutput(env.Stderr)
	host := fs.String("host", "", "hub IP or name")
	user := fs.String("user", "", "user on the hub")
	port := fs.Int("port", 0, "hub's SSH port (default 22)")
	key := fs.String("key", "", "private key (default ~/.ssh/gitsi_ed25519; created when missing)")
	alias := fs.String("alias", "gitsi-hub", "SSH alias git uses")
	hostKey := fs.String("hostkey", "", "the hub's SHA256 fingerprint (shown by 'gitsi setup hub'): verified and pinned in known_hosts")
	if err := fs.Parse(args); err != nil {
		return err
	}

	cfg := env.Config
	if *host == "" || *user == "" {
		return errors.New("usage: gitsi setup edge -host <hub-ip> -user <user-on-the-hub> [-hostkey SHA256:...]")
	}
	for _, c := range []error{safe.Name("host", *host), safe.Name("user", *user), safe.Name("alias", *alias)} {
		if c != nil {
			return c
		}
	}
	if *port < 0 || *port > 65535 {
		return fmt.Errorf("invalid port: %d (0-65535; 0 = 22)", *port)
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	sshDir := filepath.Join(home, ".ssh")
	if err := os.MkdirAll(sshDir, 0o700); err != nil {
		return err
	}

	// 1. Key: the given one or ~/.ssh/gitsi_ed25519 (created when missing).
	keyFile := *key
	if keyFile == "" {
		keyFile = filepath.Join(sshDir, "gitsi_ed25519")
	}
	if !exists(keyFile) {
		fmt.Fprintf(env.Stdout, "Generating SSH key in %s\n", keyFile)
		if err := sshKeygen(ctx, keyFile); err != nil {
			return fmt.Errorf("could not create the key: %w", err)
		}
	} else {
		fmt.Fprintf(env.Stdout, "Key: %s\n", keyFile)
	}

	// 2. ~/.ssh/config (only gitsi's block is replaced)
	if err := writeSSHConfig(sshDir, &config.Config{Hub: config.Hub{Host: *host, User: *user, Port: *port, KeyFile: keyFile, Alias: *alias}}); err != nil {
		return err
	}
	fmt.Fprintf(env.Stdout, "Updated %s (alias '%s')\n", filepath.Join(sshDir, "config"), *alias)

	// 2b. Hub fingerprint: when given, it is verified and pinned; otherwise it is shown for the user to compare.
	hk, herr := VerifyHostKey(ctx, hostScanner, *host, *port, *hostKey, filepath.Join(sshDir, "known_hosts"))
	switch {
	case herr != nil && *hostKey != "":
		if fe := fault.Classify(herr.Error(), fault.Where{Host: *host, KeyFile: keyFile}); fe != nil {
			return fe
		}
		return herr // different or unverifiable fingerprint: nothing is saved
	case hk.Pinned:
		fmt.Fprintf(env.Stdout, "Hub fingerprint verified and pinned: %s\n", hk.Fingerprint)
	case herr == nil:
		fmt.Fprintf(env.Stdout, "Hub fingerprint: %s\n  (check that it is the one 'gitsi setup hub' showed; with -hostkey it is verified and pinned automatically)\n", hk.Fingerprint)
	}

	// 3. gitsi's configuration
	cfg.Role = config.RoleEdge
	cfg.Hub = config.Hub{Host: *host, User: *user, Port: *port, KeyFile: keyFile, Alias: *alias}
	if cfg.RemoteName == "" {
		cfg.RemoteName = "hub"
	}
	if err := cfg.Save(); err != nil {
		return err
	}
	p, _ := config.Path()
	fmt.Fprintf(env.Stdout, "Configuration saved in %s\n", p)

	// 4. Test the connection
	t := &transport.SSHExec{Host: *host, User: *user, Port: *port, KeyFile: keyFile}
	res, err := t.Run(ctx, "echo gitsi-ok")
	if err == nil && strings.Contains(res.Stdout, "gitsi-ok") {
		fmt.Fprintln(env.Stdout, "Connection to the hub: OK")
		// The folder layout is decided by the hub: it is asked and cached (no foreign paths in this configuration).
		res := &hubview.Resolver{Transport: t, Config: cfg}
		if lay, hubVer, lerr := res.Refresh(ctx); lerr == nil {
			fmt.Fprintf(env.Stdout, "Hub layout (decided by the hub%s):\n", map[bool]string{true: "; its gitsi is " + hubVer, false: ""}[hubVer != ""])
			printLayout(env.Stdout, lay)
		} else {
			fmt.Fprintf(env.Stdout, "The hub's layout could not be learned yet: %s\n", describe(lerr))
		}
		return nil
	}
	// Network, host key or configuration: reported with their cause, action and exit code (the config is already saved).
	if fe, ok := fault.As(err); ok && fe.Kind != fault.Auth {
		return fe
	}
	// Unregistered key (or no recognized cause): what is missing is pairing this machine with the hub.
	fmt.Fprintln(env.Stdout, "\nCould not log in yet: this machine's key still has to be registered on the hub.")
	pubText := "<public key>"
	if pub, err := os.ReadFile(keyFile + ".pub"); err == nil {
		pubText = strings.TrimSpace(string(pub))
		fmt.Fprintf(env.Stdout, "\nPublic key (copy it to the hub):\n%s\n", pubText)
	}
	fmt.Fprintf(env.Stdout, "\nOn the hub, as Administrator:\n  gitsi setup hub -edge-ip %s -public-key \"%s\" -user %s\n", localIPToward(*host, *port), pubText, *user)
	fmt.Fprintln(env.Stdout, "\nWhen it finishes, repeat on this machine the command the hub prints (it includes the fingerprint).")
	if err != nil {
		fmt.Fprintf(env.Stdout, "Details: %v\n", err)
	} else if s := strings.TrimSpace(res.Stderr); s != "" {
		fmt.Fprintf(env.Stdout, "Details: %s\n", s)
	}
	return nil
}

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// hostScanner gets the key the hub presents; a variable so tests can replace it.
var hostScanner Scanner = SystemScanner

// localIPToward returns the IP this machine uses toward the hub (the one the hub will see), to give the complete command. It
// sends nothing: connecting a UDP socket only consults the routing table.
func localIPToward(host string, port int) string {
	if port == 0 {
		port = 22
	}
	c, err := net.Dial("udp", net.JoinHostPort(host, fmt.Sprint(port)))
	if err != nil {
		return "<this-machine-ip>"
	}
	defer c.Close()
	if a, ok := c.LocalAddr().(*net.UDPAddr); ok && a.IP != nil {
		return a.IP.String()
	}
	return "<this-machine-ip>"
}
