package provision

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/ShizukaJiku/gitsi/internal/config"
	"github.com/ShizukaJiku/gitsi/internal/module"
	"github.com/ShizukaJiku/gitsi/internal/transport"
)

// KeyOptions are the data of `gitsi setup key`.
type KeyOptions struct {
	SSHDir    string // ~/.ssh
	NewKey    string // new key (default ~/.ssh/gitsi_ed25519)
	OldKey    string // key to retire with Retire (default: the one used before the change)
	Retire    bool   // remove the previous key from the hub, once the new one works
	ConfigSSH bool   // rewrite gitsi's block in ~/.ssh/config (git uses it)
}

// KeyDeps is what `setup key` needs from the world, injectable to test it.
type KeyDeps struct {
	Current transport.Transport                      // connection with the current key
	Dial    func(keyFile string) transport.Transport // connection with another key (IdentitiesOnly: it really tests that key)
	Keygen  func(ctx context.Context, path string) error
	Out     io.Writer
}

// hubKeyFile is the PowerShell fragment that picks the key file sshd uses for the session's user: members of Administrators
// (SID S-1-5-32-544, which works with any Windows language) use administrators_authorized_keys; everyone else, their profile's.
const hubKeyFile = `$ErrorActionPreference = 'Stop'
if ((whoami /groups) -match 'S-1-5-32-544') { $f = Join-Path $env:ProgramData 'ssh\administrators_authorized_keys' } else { $f = Join-Path $env:USERPROFILE '.ssh\authorized_keys' }
if (-not (Test-Path -LiteralPath $f)) { 'MISSING ' + $f; exit 0 }
$nl = [Environment]::NewLine
`

// addKeyScript adds the public key when missing. It writes over the existing file (keeping its permissions) and without a BOM.
func addKeyScript(pub string) string {
	blob := strings.Fields(pub)[1]
	return hubKeyFile + fmt.Sprintf(`$k = '%s'
$lines = @(Get-Content -LiteralPath $f)
if (@($lines | Where-Object { $_.Contains('%s') }).Count -gt 0) { 'EXISTS ' + $f; exit 0 }
$raw = [IO.File]::ReadAllText($f)
$pre = ''
if ($raw.Length -gt 0 -and -not $raw.EndsWith([string][char]10)) { $pre = $nl }
[IO.File]::AppendAllText($f, $pre + $k + $nl)
'ADDED ' + $f`, pub, blob)
}

// removeKeyScript removes the old key's lines, only when the new one is in the same file (it never locks the user out).
func removeKeyScript(oldBlob, newBlob string) string {
	return hubKeyFile + fmt.Sprintf(`$lines = @(Get-Content -LiteralPath $f)
if (@($lines | Where-Object { $_.Contains('%s') }).Count -eq 0) { 'NONEW ' + $f; exit 0 }
$keep = @($lines | Where-Object { -not $_.Contains('%s') })
$n = $lines.Count - $keep.Count
if ($n -eq 0) { 'NOTFOUND ' + $f; exit 0 }
[IO.File]::WriteAllText($f, ($keep -join $nl) + $nl)
'REMOVED ' + $n + ' ' + $f`, newBlob, oldBlob)
}

// pubBlob reads a public key (.pub) and returns the line and its base64 part, validated (they end up inside a script).
func pubBlob(pubPath string) (line, blob string, err error) {
	b, err := os.ReadFile(pubPath)
	if err != nil {
		return "", "", fmt.Errorf("could not read the public key %s: %w", pubPath, err)
	}
	line = strings.TrimSpace(strings.SplitN(strings.ReplaceAll(string(b), "\r\n", "\n"), "\n", 2)[0])
	if !pubKeyRe.MatchString(line) || strings.ContainsAny(line, "'\"`$") {
		return "", "", fmt.Errorf("%s is not a valid public key", pubPath)
	}
	return line, strings.Fields(line)[1], nil
}

// hubReply looks for the status word the hub's scripts return (ADDED, EXISTS, ...).
func hubReply(res transport.Result) (word, rest string) {
	for _, l := range strings.Split(res.Stdout, "\n") {
		l = strings.TrimSpace(l)
		for _, w := range []string{"ADDED", "EXISTS", "MISSING", "REMOVED", "NOTFOUND", "NONEW"} {
			if strings.HasPrefix(l, w+" ") {
				return w, strings.TrimSpace(strings.TrimPrefix(l, w))
			}
		}
	}
	return "", strings.TrimSpace(res.Stdout + " " + res.Stderr)
}

func loginWorks(ctx context.Context, t transport.Transport) error {
	res, err := t.Run(ctx, "echo gitsi-ok")
	if err != nil {
		return err
	}
	if !strings.Contains(res.Stdout, "gitsi-ok") {
		return fmt.Errorf("unexpected answer from the hub: %s", strings.TrimSpace(res.Stdout+" "+res.Stderr))
	}
	return nil
}

func samePath(a, b string) bool {
	return strings.EqualFold(filepath.Clean(filepath.FromSlash(a)), filepath.Clean(filepath.FromSlash(b)))
}

// RotateKey swaps the edge's key for a gitsi one without losing access at any moment:
//  1. it creates the new key (when missing) and registers it on the hub using the current connection;
//  2. it tests a login with ONLY the new one; if that fails, nothing changes on this machine;
//  3. it saves the new one in gitsi's configuration and in ~/.ssh/config;
//  4. with Retire, and only then, it removes the previous key from the hub and tests the new one again.
//
// It returns the key left in use.
func RotateKey(ctx context.Context, d KeyDeps, cfg *config.Config, o KeyOptions) (string, error) {
	current := cfg.Hub.KeyFile
	if current == "" || cfg.Hub.Host == "" {
		return "", errors.New("this machine has no hub configured: gitsi setup edge -host <ip> -user <user>")
	}
	newKey := o.NewKey
	if newKey == "" {
		newKey = filepath.Join(o.SSHDir, "gitsi_ed25519")
	}
	oldKey := o.OldKey

	if samePath(current, newKey) {
		fmt.Fprintf(d.Out, "You already use the key %s.\n", newKey)
		if !o.Retire {
			return newKey, nil
		}
		if oldKey == "" {
			oldKey = cfg.Hub.PrevKeyFile // the one used before the last 'setup key'
		}
		if oldKey == "" {
			return newKey, errors.New("there is no previous key to retire (say which one with -old)")
		}
	} else {
		if oldKey == "" {
			oldKey = current
		}
		if !exists(newKey) {
			fmt.Fprintf(d.Out, "Generating the new key %s\n", newKey)
			if err := d.Keygen(ctx, newKey); err != nil {
				return "", fmt.Errorf("could not create the key: %w", err)
			}
		} else {
			fmt.Fprintf(d.Out, "New key (it already existed): %s\n", newKey)
		}
		pub, _, err := pubBlob(newKey + ".pub")
		if err != nil {
			return "", err
		}

		// 1. Register it on the hub with the key that works today
		res, err := d.Current.Run(ctx, addKeyScript(pub))
		if err != nil {
			return "", fmt.Errorf("could not register the new key on the hub (you keep %s): %w", current, err)
		}
		switch w, rest := hubReply(res); w {
		case "ADDED":
			fmt.Fprintf(d.Out, "Registered on the hub: %s\n", rest)
		case "EXISTS":
			fmt.Fprintf(d.Out, "Already registered on the hub: %s\n", rest)
		case "MISSING":
			return "", fmt.Errorf("%s, this user's key file, does not exist on the hub; nothing was changed", rest)
		default:
			return "", fmt.Errorf("unexpected answer from the hub while registering the key (exit code %d): %s", res.ExitCode, rest)
		}

		// 2. Test a login with only the new one
		if err := loginWorks(ctx, d.Dial(newKey)); err != nil {
			return "", fmt.Errorf("the new key is registered but logging in with it failed; this machine keeps using %s: %w", current, err)
		}
		fmt.Fprintln(d.Out, "Login with the new key: OK")

		// 3. Use it (and remember the previous one to retire it later with -retire-old)
		cfg.Hub.PrevKeyFile = current
		cfg.Hub.KeyFile = newKey
		if err := cfg.Save(); err != nil {
			return "", err
		}
		if o.ConfigSSH {
			if err := writeSSHConfig(o.SSHDir, cfg); err != nil {
				return "", fmt.Errorf("gitsi's configuration was updated, but not ~/.ssh/config (git would keep the previous key): %w", err)
			}
			fmt.Fprintf(d.Out, "Updated gitsi's configuration and %s\n", filepath.Join(o.SSHDir, "config"))
		} else {
			fmt.Fprintln(d.Out, "Updated gitsi's configuration")
		}
	}

	if !o.Retire {
		fmt.Fprintf(d.Out, "\nThe previous key (%s) stays registered on the hub as a fallback.\nWhen you confirm everything works (e.g. gitsi check), remove it with:  gitsi setup key -retire-old\n", oldKey)
		return newKey, nil
	}

	// 4. Retire the previous one
	if samePath(oldKey, newKey) {
		return newKey, errors.New("the key to retire is the one in use")
	}
	_, oldBlob, err := pubBlob(oldKey + ".pub")
	if err != nil {
		return newKey, fmt.Errorf("could not identify the previous key (give its path with -old): %w", err)
	}
	_, newBlob, err := pubBlob(newKey + ".pub")
	if err != nil {
		return newKey, err
	}
	nt := d.Dial(newKey)
	res, err := nt.Run(ctx, removeKeyScript(oldBlob, newBlob))
	if err != nil {
		return newKey, fmt.Errorf("could not remove the previous key from the hub: %w", err)
	}
	switch w, rest := hubReply(res); w {
	case "REMOVED":
		fmt.Fprintf(d.Out, "Previous key removed from the hub (%s)\n", rest)
	case "NOTFOUND":
		fmt.Fprintf(d.Out, "The previous key was no longer registered on the hub (%s)\n", rest)
	case "NONEW":
		return newKey, fmt.Errorf("the new key is not in %s: the previous one is not removed so you keep access", rest)
	case "MISSING":
		return newKey, fmt.Errorf("%s does not exist on the hub", rest)
	default:
		return newKey, fmt.Errorf("unexpected answer from the hub while retiring the key (exit code %d): %s", res.ExitCode, rest)
	}
	if err := loginWorks(ctx, nt); err != nil {
		return newKey, fmt.Errorf("after retiring the previous one, logging in with the new one failed (check the hub as administrator): %w", err)
	}
	if samePath(cfg.Hub.PrevKeyFile, oldKey) {
		cfg.Hub.PrevKeyFile = ""
		if err := cfg.Save(); err != nil {
			return newKey, err
		}
	}
	fmt.Fprintf(d.Out, "Login with the new key: OK. The previous one no longer gives access; its files are still in %s(.pub) and you can delete them.\n", oldKey)
	return newKey, nil
}

func sshKeygen(ctx context.Context, path string) error {
	out, err := exec.CommandContext(ctx, "ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-f", path, "-C", "gitsi").CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// setupKeyCmd is `gitsi setup key`.
func setupKeyCmd(ctx context.Context, env *module.Env, args []string) error {
	fs := module.NewFlagSet("setup key", env.Stderr)
	newKey := fs.String("key", "", "new key (default ~/.ssh/gitsi_ed25519; created when missing)")
	oldKey := fs.String("old", "", "key to retire with -retire-old (default: the one used before the change)")
	retire := fs.Bool("retire-old", false, "remove the previous key from the hub once the new one is checked")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return errors.New("usage: gitsi setup key [-retire-old] [-key <path>] [-old <path>]")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	h := env.Config.Hub
	d := KeyDeps{
		Current: env.Transport,
		Dial: func(k string) transport.Transport {
			return &transport.SSHExec{Host: h.Host, User: h.User, Port: h.Port, KeyFile: k}
		},
		Keygen: sshKeygen,
		Out:    env.Stdout,
	}
	_, err = RotateKey(ctx, d, env.Config, KeyOptions{
		SSHDir: filepath.Join(home, ".ssh"), NewKey: *newKey, OldKey: *oldKey, Retire: *retire, ConfigSSH: true,
	})
	return err
}
