package vpn

import (
	"context"
	"log/slog"
	"strings"

	"github.com/docker/docker/api/types/network"
	dockerclient "github.com/docker/docker/client"
)

// effectiveDockerNetwork resolves the Docker network whose subnets Gluetun must
// be allowed to talk to.
//
// An unset DOCKER_NETWORK is not "no network": Docker attaches the container to
// the default `bridge` network instead. Treating empty as "nothing to do" is
// what turned the firewall allowlist into a silent no-op on every
// default-configured install. Host and none networking genuinely have no bridge
// subnet to allow, so those return "".
func effectiveDockerNetwork(configured string) string {
	switch n := strings.TrimSpace(configured); n {
	case "":
		return "bridge"
	case "host", "none":
		return ""
	default:
		return n
	}
}

// subnetsFromIPAM extracts the non-empty, de-duplicated subnets of a Docker
// network's IPAM configuration, preserving order.
func subnetsFromIPAM(cfgs []network.IPAMConfig) []string {
	var out []string
	seen := make(map[string]bool, len(cfgs))
	for _, c := range cfgs {
		s := strings.TrimSpace(c.Subnet)
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

// applyFirewallSubnetAllowlist sets FIREWALL_OUTBOUND_SUBNETS on a Gluetun
// container so it can reach — and be reached by — its sibling containers.
//
// Without it, Gluetun's firewall drops traffic to and from the Docker network
// once the VPN tunnel is up, so the orchestrator's control-API health probe
// times out even though the tunnel itself (and Docker's own healthcheck) is
// fine. An explicit value already present in envMap always wins.
//
// Every path that ends without setting the variable logs why. This used to fail
// silently, which made a misconfigured network indistinguishable from a genuine
// network fault.
func applyFirewallSubnetAllowlist(
	ctx context.Context,
	cli dockerclient.NetworkAPIClient,
	configuredNetwork string,
	envMap map[string]string,
) {
	const key = "FIREWALL_OUTBOUND_SUBNETS"

	if v, ok := envMap[key]; ok && strings.TrimSpace(v) != "" {
		slog.Debug("firewall subnet allowlist already set explicitly", "subnets", v)
		return
	}

	netName := effectiveDockerNetwork(configuredNetwork)
	if netName == "" {
		slog.Debug("skipping firewall subnet allowlist: host/none networking",
			"docker_network", configuredNetwork)
		return
	}

	netInfo, err := cli.NetworkInspect(ctx, netName, network.InspectOptions{})
	if err != nil {
		slog.Warn("firewall subnet allowlist not applied: docker network inspect failed; "+
			"Gluetun may drop the orchestrator's control-API probes once the tunnel is up",
			"network", netName, "err", err)
		return
	}

	subnets := subnetsFromIPAM(netInfo.IPAM.Config)
	if len(subnets) == 0 {
		slog.Warn("firewall subnet allowlist not applied: docker network reports no IPAM subnets",
			"network", netName)
		return
	}

	envMap[key] = strings.Join(subnets, ",")
	slog.Info("firewall subnet allowlist applied to Gluetun container",
		"network", netName, "subnets", envMap[key])
}
