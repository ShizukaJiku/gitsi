package services

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/ShizukaJiku/gitsi/internal/config"
	"github.com/ShizukaJiku/gitsi/internal/module"
	"github.com/ShizukaJiku/gitsi/internal/safe"
)

// dockerPsScript runs on the hub: it only lists the running containers and their ports (it changes nothing). Format: name|ports.
const dockerPsScript = `docker ps --format '{{.Names}}|{{.Ports}}'`

// dockerServices asks the hub which TCP ports its containers publish and returns them as services with the same local port:
// `svc up -docker` opens them without going through the catalog (they are not saved).
func dockerServices(ctx context.Context, env *module.Env) ([]config.Service, error) {
	res, err := env.RunOnHub(ctx, dockerPsScript)
	if err != nil {
		return nil, err
	}
	if res.ExitCode != 0 {
		return nil, fmt.Errorf("the hub could not list its containers (is Docker running on the hub?): %s", strings.TrimSpace(res.Stderr+" "+res.Stdout))
	}
	return parseDockerPorts(res.Stdout), nil
}

// parseDockerPorts interprets dockerPsScript's output. From each container it takes the TCP ports published on the hub
// ("0.0.0.0:5432->5432/tcp", "[::]:5432->5432/tcp", "127.0.0.1:8080->80/tcp"); unpublished ones ("5432/tcp"), UDP and ranges
// are skipped. A port that shows up for IPv4 and IPv6 counts once. A name that does not pass safe.Name is skipped.
func parseDockerPorts(out string) []config.Service {
	seen := map[int]bool{}
	var svcs []config.Service
	for _, line := range strings.Split(strings.ReplaceAll(out, "\r", ""), "\n") {
		name, ports, ok := strings.Cut(strings.TrimSpace(line), "|")
		if !ok || safe.Name("container", name) != nil {
			continue
		}
		var mine []int
		for _, p := range strings.Split(ports, ",") {
			p = strings.TrimSpace(p)
			host, target, ok := strings.Cut(p, "->")
			if !ok || !strings.HasSuffix(target, "/tcp") {
				continue
			}
			port, err := strconv.Atoi(host[strings.LastIndex(host, ":")+1:])
			if err != nil || port <= 0 || port > 65535 || seen[port] {
				continue
			}
			seen[port] = true
			mine = append(mine, port)
		}
		sort.Ints(mine)
		for i, port := range mine {
			n := name
			if i > 0 {
				n = name + "-" + strconv.Itoa(port) // a container with several ports: each tunnel with its own name
			}
			svcs = append(svcs, config.Service{Name: n, RemotePort: port, LocalPort: port})
		}
	}
	return svcs
}
