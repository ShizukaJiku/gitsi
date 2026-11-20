package provision

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"github.com/ShizukaJiku/gitsi/internal/fsx"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/ShizukaJiku/gitsi/internal/fault"
	"github.com/ShizukaJiku/gitsi/internal/layout"
)

// The hub's host key fingerprint lets the edge know it talks to the hub and not to another machine. Without it, ssh accepts the
// first key it sees (trust on first use). With `setup edge -hostkey SHA256:...` the fingerprint `setup hub` printed is compared
// and pinned.

// Fingerprint computes the SHA256 fingerprint of an OpenSSH public key, like `ssh-keygen -lf`. It accepts "type base64 [comment]"
// and the known_hosts/ssh-keyscan format "host type base64".
func Fingerprint(keyLine string) (string, error) {
	f := strings.Fields(keyLine)
	for i, w := range f {
		if (strings.HasPrefix(w, "ssh-") || strings.HasPrefix(w, "ecdsa-") || strings.HasPrefix(w, "sk-")) && i+1 < len(f) {
			blob, err := base64.StdEncoding.DecodeString(f[i+1])
			if err != nil || len(blob) == 0 {
				return "", fmt.Errorf("invalid public key: %v", err)
			}
			sum := sha256.Sum256(blob)
			return "SHA256:" + base64.RawStdEncoding.EncodeToString(sum[:]), nil
		}
	}
	return "", fmt.Errorf("no OpenSSH public key recognized in %.40q", keyLine)
}

// NormalizeFingerprint accepts the fingerprint with or without the "SHA256:" prefix and returns it with the prefix.
func NormalizeFingerprint(s string) string {
	s = strings.TrimSpace(s)
	if s != "" && !strings.HasPrefix(strings.ToUpper(s), "SHA256:") {
		return "SHA256:" + s
	}
	if strings.HasPrefix(strings.ToUpper(s), "SHA256:") {
		return "SHA256:" + s[len("SHA256:"):]
	}
	return s
}

// Scanner gets the public key a hub presents (ssh-keyscan). It is injected to test without a network.
type Scanner func(ctx context.Context, host string, port int) (string, error)

// SystemScanner uses the system's ssh-keyscan, without authentication and with a deadline.
func SystemScanner(ctx context.Context, host string, port int) (string, error) {
	args := []string{"-t", "ed25519", "-T", "8"}
	if port != 0 {
		args = append(args, "-p", strconv.Itoa(port))
	}
	args = append(args, host)
	cctx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	out, err := exec.CommandContext(cctx, "ssh-keyscan", args...).Output() // stdout: the key; stderr: comments
	if err != nil && len(out) == 0 {
		return "", fmt.Errorf("ssh-keyscan: %w", err)
	}
	for _, l := range strings.Split(string(out), "\n") {
		l = strings.TrimSpace(l)
		if l != "" && !strings.HasPrefix(l, "#") {
			return l, nil
		}
	}
	return "", fmt.Errorf("the hub presented no ed25519 key (is sshd running on %s:%d?)", host, portOr22(port))
}

func portOr22(p int) int {
	if p == 0 {
		return 22
	}
	return p
}

// HostKeyResult describes what happened when checking the fingerprint.
type HostKeyResult struct {
	Fingerprint string // the one the hub presents
	Pinned      bool   // verified against the expected one and pinned in known_hosts
}

// VerifyHostKey gets the key the hub presents and, when an expected fingerprint was given, compares it and pins it in
// known_hosts. Without an expected fingerprint it changes nothing: it only returns the fingerprint for the user to compare.
func VerifyHostKey(ctx context.Context, scan Scanner, host string, port int, expected, knownHosts string) (HostKeyResult, error) {
	line, err := scan(ctx, host, port)
	if err != nil {
		return HostKeyResult{}, err
	}
	fp, err := Fingerprint(line)
	if err != nil {
		return HostKeyResult{}, err
	}
	res := HostKeyResult{Fingerprint: fp}
	if expected == "" {
		return res, nil
	}
	if want := NormalizeFingerprint(expected); want != fp {
		return res, fault.New(fault.HostKey,
			fmt.Sprintf("the host key %s presents (%s) does not match the expected one (%s)", host, fp, want),
			"stop: it may not be your hub. If you reinstalled it, copy the new fingerprint from 'gitsi setup hub' on the hub")
	}
	if err := PinKnownHosts(knownHosts, line); err != nil {
		return res, err
	}
	res.Pinned = true
	return res, nil
}

// PinKnownHosts adds the key to known_hosts when it was not there. When there is ANOTHER key of the same type for that host, it
// fails: a fingerprint the user already accepted is never overwritten.
func PinKnownHosts(path, line string) error {
	f := strings.Fields(line)
	if len(f) < 3 {
		return fmt.Errorf("invalid key line: %.40q", line)
	}
	host, typ, blob := f[0], f[1], f[2]
	var existing string
	if b, err := os.ReadFile(path); err == nil {
		existing = string(b)
		for _, l := range strings.Split(strings.ReplaceAll(existing, "\r", ""), "\n") {
			lf := strings.Fields(l)
			if len(lf) >= 3 && lf[0] == host && lf[1] == typ {
				if lf[2] == blob {
					return nil // already there
				}
				return fault.New(fault.HostKey, "known_hosts already has another "+typ+" key for "+host,
					"if the hub was reinstalled: ssh-keygen -R "+host+" and repeat the command; otherwise, stop")
			}
		}
	}
	if existing != "" && !strings.HasSuffix(existing, "\n") {
		existing += "\n"
	}
	return fsx.WriteFileAtomic(path, []byte(existing+line+"\n"), 0o600)
}

// hubHostFingerprint reads the hub's host key (ed25519) and returns its fingerprint; the hub shows it at the end of `setup hub`.
func hubHostFingerprint(sys System, programData string) (string, error) {
	b, err := sys.ReadFile(filepath.Join(programData, "ssh", "ssh_host_ed25519_key.pub"))
	if err != nil {
		return "", err
	}
	return Fingerprint(string(b))
}

// pickIP picks the hub IP on the same network as the edge (same first 3 octets); otherwise, the first one.
func pickIP(ips []string, edgeIP string) string {
	if len(ips) == 0 {
		return "<hub-ip>"
	}
	prefix := edgeIP
	if i := strings.LastIndex(edgeIP, "."); i > 0 {
		prefix = edgeIP[:i+1]
	}
	for _, ip := range ips {
		if strings.HasPrefix(ip, prefix) {
			return ip
		}
	}
	return ips[0]
}

// pairingGuidance is what `setup hub` prints at the end: the edge's exact command, with the fingerprint to verify and pin it.
func pairingGuidance(fingerprint, hubIP, user string) string {
	nl := string(rune(10))
	cmd := "  gitsi setup edge -host " + hubIP + " -user " + user
	if fingerprint == "" {
		return nl + "On the edge, run:" + nl + cmd + nl
	}
	return nl + "This hub's fingerprint: " + fingerprint + nl + nl + "On the edge, run:" + nl + cmd + " -hostkey " + fingerprint + nl
}

// EnsureHubDirs creates gitsi's folders on the hub (repos, mailbox, jobs) when missing.
func EnsureHubDirs(lay layout.Layout) error {
	for _, d := range []string{lay.Home, lay.Repos, lay.Mailbox, lay.Jobs} {
		if err := os.MkdirAll(filepath.FromSlash(d), 0o755); err != nil {
			return err
		}
	}
	return nil
}

// printLayout shows the hub's layout, one path per line.
func printLayout(w io.Writer, l layout.Layout) {
	fmt.Fprintln(w, "  repos:    "+l.Repos)
	fmt.Fprintln(w, "  mailbox:  "+l.Mailbox)
	fmt.Fprintln(w, "  gitsi:    "+l.Binary)
}

// describe summarizes an error (with its action when it is a fault) in one line.
func describe(err error) string {
	if fe, ok := fault.As(err); ok && fe.Hint != "" {
		return fe.Error() + " -> " + fe.Hint
	}
	return err.Error()
}
