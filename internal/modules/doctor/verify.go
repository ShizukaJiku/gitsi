package doctor

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/ShizukaJiku/gitsi/internal/config"
	"github.com/ShizukaJiku/gitsi/internal/hubclient"
	"github.com/ShizukaJiku/gitsi/internal/layout"
	"github.com/ShizukaJiku/gitsi/internal/mailbox"
	"github.com/ShizukaJiku/gitsi/internal/module"
	"github.com/ShizukaJiku/gitsi/internal/policy"
	"github.com/ShizukaJiku/gitsi/internal/transport"
	"github.com/ShizukaJiku/gitsi/internal/version"
)

// verify is the end-to-end verification run on the real EDGE: it gathers everything that cannot be tested from another machine
// (network, firewall, the SSH session's PATH, binaries) in a report that can be pasted as is. By default it changes nothing;
// with -active it also tests sending and remote running, only when the policy already allows them.
func verify(ctx context.Context, env *module.Env, args []string) error {
	fs := flag.NewFlagSet("check", flag.ContinueOnError)
	fs.SetOutput(env.Stderr)
	active := fs.Bool("active", false, "also send a test text to the mailbox and run 'hostname' (only when the send/exec policy is enabled)")
	out := fs.String("out", "", "report file (default ~/.gitsi/verify-report.txt)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	var buf bytes.Buffer
	fresh := func(ctx context.Context) (layout.Layout, error) {
		l, _, err := env.RefreshHubLayout(ctx)
		return l, err
	} // a diagnosis does not use the cache
	err := runVerify(ctx, &buf, env.Config, env.Transport, systemDeps(),
		VerifyOptions{Active: *active, Tunnel: true, Mode: Mode{Quiet: env.Quiet, JSON: env.JSON}, Layout: fresh})

	path := *out
	if path == "" {
		if home, herr := os.UserHomeDir(); herr == nil {
			path = filepath.Join(home, ".gitsi", "verify-report.txt")
		}
	}
	fmt.Fprint(env.Stdout, buf.String())
	// with --json stdout is only the report: side notes go to stderr, and with -q they are not shown
	note := env.Stdout
	if env.JSON {
		note = env.Stderr
	}
	if path != "" {
		if werr := os.MkdirAll(filepath.Dir(path), 0o700); werr == nil && os.WriteFile(path, buf.Bytes(), 0o600) == nil && !env.Quiet {
			fmt.Fprintln(note, "")
			fmt.Fprintln(note, "Report saved in "+path)
		}
	}
	if !env.Quiet {
		fmt.Fprintln(note, "Review the report before sharing it: it includes IPs, paths and tool names (no keys or content).")
	}
	return err
}

// VerifyOptions are runVerify's options.
type VerifyOptions struct {
	Active bool       // also tests that write (send, run), when the policy allows it
	Tunnel bool       // test a -L tunnel
	Mode   Mode       // output: normal, -q or --json
	Layout LayoutFunc // discovers the hub's layout (the hub announces it)
}

// runVerify writes the report to w and returns an error when any check failed.
func runVerify(ctx context.Context, w io.Writer, cfg *config.Config, tr transport.Transport, d Deps, vo VerifyOptions) error {
	if !vo.Mode.JSON {
		fmt.Fprintln(w, "=== gitsi check ===")
		fmt.Fprintln(w, "gitsi "+version.String()+"  "+runtime.GOOS+"/"+runtime.GOARCH+"  "+time.Now().Format("2006-01-02 15:04:05"))
		fmt.Fprintln(w)
	}

	rs := EdgeChecks(ctx, cfg, tr, d, Options{Tunnel: vo.Tunnel, Serve: true, Layout: vo.Layout})
	if cfg.Hub.Host != "" && tr != nil {
		rs = append(rs, updateCheck(ctx, cfg, tr, vo))
	}
	if vo.Active {
		rs = append(rs, activeChecks(ctx, cfg, tr)...)
	}
	return report(w, rs, vo.Mode)
}

// updateCheck compares the binary published on the hub with the one running.
func updateCheck(ctx context.Context, cfg *config.Config, tr transport.Transport, vo VerifyOptions) Result {
	lay, err := resolveLayout(ctx, cfg, Options{Layout: vo.Layout})
	if err != nil {
		return Result{Warn, "gitsi published on the hub", "could not learn where the hub publishes it: " + describe(err)}
	}
	remote := lay.Binary
	res, err := tr.Run(ctx, "(Get-FileHash -Algorithm SHA256 -LiteralPath '"+remote+"').Hash")
	want := strings.ToLower(strings.TrimSpace(res.Stdout))
	if err != nil || res.ExitCode != 0 || len(want) != 64 {
		return Result{Warn, "gitsi published on the hub", "no binary at " + remote + ": on the hub, 'gitsi update -publish'"}
	}
	self, err := os.Executable()
	if err != nil {
		return Result{Info, "gitsi published on the hub", "could not read the local executable"}
	}
	b, err := os.ReadFile(self)
	if err != nil {
		return Result{Info, "gitsi published on the hub", err.Error()}
	}
	sum := sha256.Sum256(b)
	if hex.EncodeToString(sum[:]) == want {
		return Result{Pass, "gitsi published on the hub", "same as the local one (" + version.String() + ")"}
	}
	// different: say which is which (version and build) to know which side is outdated
	hubVer := "unknown version"
	if vr, verr := tr.Run(ctx, "& '"+remote+"' version"); verr == nil && vr.ExitCode == 0 {
		if v := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(vr.Stdout), "gitsi")); v != "" {
			hubVer = v
		}
	}
	return Result{Warn, "gitsi published on the hub", "different from the local one: hub " + hubVer + ", this machine " + version.String() + ". To match them: 'gitsi update' (or 'gitsi update -publish' on the hub)"}
}

// activeChecks tests what changes something, only when the policy already allows it (it never enables it on its own).
func activeChecks(ctx context.Context, cfg *config.Config, tr transport.Transport) []Result {
	var rs []Result

	if policy.Check(cfg.Policy, policy.Send) != nil {
		rs = append(rs, Result{Info, "send test", "skipped: 'send' is off (enable it with: gitsi policy set send on)"})
	} else {
		rs = append(rs, sendProbe(ctx, cfg, tr))
	}

	if policy.Check(cfg.Policy, policy.Exec) != nil {
		rs = append(rs, Result{Info, "run test", "skipped: 'exec' is off (enable it with: gitsi policy set exec on)"})
	} else if ex, ok := tr.(transport.Executor); !ok {
		rs = append(rs, Result{Fail, "run test", "the transport does not support running with live output"})
	} else {
		var out, errb bytes.Buffer
		code, err := ex.Exec(ctx, "hostname", &out, &errb)
		switch {
		case err != nil:
			rs = append(rs, Result{Fail, "run test", err.Error()})
		case code != 0:
			rs = append(rs, Result{Fail, "run test", fmt.Sprintf("'hostname' returned %d: %s", code, strings.TrimSpace(errb.String()))})
		default:
			rs = append(rs, Result{Pass, "run test", "the hub answered 'hostname': " + strings.TrimSpace(out.String())})
		}
	}
	return rs
}

// sendProbe uploads a tiny text to the inbox; Put ending well means the hub received it and verified its SHA256.
func sendProbe(ctx context.Context, cfg *config.Config, tr transport.Transport) Result {
	c, err := hubclient.Dial(ctx, tr, mailbox.CapDrop)
	if err != nil {
		return Result{Fail, "send test", err.Error()}
	}
	defer c.Close()
	if !c.Has(mailbox.CapDrop) {
		return Result{Fail, "send test", "the hub does not offer the mailbox: on the hub: gitsi update -publish (and, if it was never set up, gitsi setup hub)"}
	}
	text := "gitsi check " + time.Now().Format(time.RFC3339)
	it, err := mailbox.NewItem(mailbox.Text, "verify.txt", strings.NewReader(text), int64(len(text)))
	if err != nil {
		return Result{Fail, "send test", err.Error()}
	}
	if _, err := mailbox.NewClient(c.RW).Put(ctx, it, strings.NewReader(text), nil); err != nil {
		return Result{Fail, "send test", err.Error()}
	}
	return Result{Pass, "send test", "the hub received and verified item " + it.ID + " (on the hub: gitsi take -list)"}
}
