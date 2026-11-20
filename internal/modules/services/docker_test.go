package services

import (
	"context"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/ShizukaJiku/gitsi/internal/transport"
)

func TestParseDockerPortsTakesPublishedTCPOnce(t *testing.T) {
	out := "db|0.0.0.0:5432->5432/tcp, [::]:5432->5432/tcp\r\n" +
		"web|127.0.0.1:8080->80/tcp, 0.0.0.0:8443->443/tcp\r\n" +
		"cache|6379/tcp\r\n" + // not published
		"dns|0.0.0.0:53->53/udp\r\n" +
		"range|0.0.0.0:9000-9001->9000-9001/tcp\r\n" +
		"bad name|0.0.0.0:7000->7000/tcp\r\n" +
		"\r\n"
	got := parseDockerPorts(out)
	var s []string
	for _, x := range got {
		s = append(s, fmt.Sprintf("%s:%d->%d", x.Name, x.LocalPort, x.RemotePort))
	}
	if strings.Join(s, " ") != "db:5432->5432 web:8080->8080 web-8443:8443->8443" {
		t.Fatalf("%v", s)
	}
}

// dockerTr answers docker ps like the hub and records the requested tunnels.
type dockerTr struct {
	fakeTr
	ps     string
	code   int
	script string
}

func (d *dockerTr) Run(_ context.Context, script string) (transport.Result, error) {
	d.script = script
	return transport.Result{Stdout: d.ps, ExitCode: d.code, Stderr: "docker: command not found"}, nil
}

func TestUpDockerOpensATunnelPerPublishedPortAndSkipsBusyOnes(t *testing.T) {
	free := freeLocalPort()
	busy, _ := net.Listen("tcp", "127.0.0.1:0")
	defer busy.Close()
	busyPort := busy.Addr().(*net.TCPAddr).Port
	tr := &dockerTr{ps: fmt.Sprintf("db|0.0.0.0:%d->5432/tcp\nweb|0.0.0.0:%d->80/tcp\n", free, busyPort)}
	e, out := env(t, tr)
	e.Config.Policy.AllowServices = true

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- upCmd(ctx, e, []string{"-docker"}) }()
	time.Sleep(200 * time.Millisecond)
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(tr.script, "docker ps") || len(tr.got) != 1 || tr.got[0].LocalPort != free || tr.got[0].RemotePort != free {
		t.Fatalf("one tunnel per published port (same port on both sides): %+v", tr.got)
	}
	if !strings.Contains(out.String(), fmt.Sprintf("web skipped: local port %d is already in use", busyPort)) {
		t.Fatalf("the busy one is warned about and skipped:\n%s", out)
	}
	if len(e.Config.Services) != 0 {
		t.Fatal("-docker does not touch the catalog")
	}
}

func TestUpDockerErrors(t *testing.T) {
	e, _ := env(t, &dockerTr{code: 1})
	if err := upCmd(context.Background(), e, []string{"-docker"}); err == nil || !strings.Contains(err.Error(), "Docker running") {
		t.Fatalf("without docker on the hub: %v", err)
	}
	e, _ = env(t, &dockerTr{ps: "cache|6379/tcp\n"})
	if err := upCmd(context.Background(), e, []string{"-docker"}); err == nil || !strings.Contains(err.Error(), "no hub container") {
		t.Fatalf("nothing published: %v", err)
	}
	if err := upCmd(context.Background(), e, []string{"-docker", "postgres"}); err == nil || !strings.Contains(err.Error(), "does not combine") {
		t.Fatalf("%v", err)
	}
}
