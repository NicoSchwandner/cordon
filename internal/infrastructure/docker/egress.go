package docker

import (
	"context"
	"fmt"
	"io"
	"log"
	"strings"

	"github.com/docker/docker/api/types/container"
	dockerimage "github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/network"
)

const (
	gatewayImage     = "alpine:3.20"
	gatewayPort      = "8443"
	gatewayLabelKey  = "cordon.egress-gateway"
	gatewayLabelVal  = "true"
	gatewayNetSuffix = "-gw"
)

// EnsureProxyAccess makes the Cordon proxy reachable from an isolated (internal)
// workspace network by starting a gateway container that bridges the internal
// network to the proxy. The gateway is the only path out — workspace containers
// cannot modify or bypass it because the network-level isolation is enforced by
// Docker, outside the container's control.
//
// Architecture:
//
//	[workspace container] --(internal network)--> [gateway] --(bridge network)--> [Cordon proxy]
//
// The gateway runs socat to forward TCP from its internal-network IP to the
// external proxy address. Returns the gateway's internal address (host:port).
func (b *Backend) EnsureProxyAccess(ctx context.Context, networkName string, proxyAddr string, labels map[string]string) (string, error) {
	gwName := gatewayName(networkName)

	log.Printf("[docker] ensuring proxy access: gateway=%s proxy=%s", gwName, proxyAddr)

	// Pull the lightweight gateway image
	if err := b.pullIfMissing(ctx, gatewayImage); err != nil {
		return "", fmt.Errorf("pulling gateway image: %w", err)
	}

	// Parse proxy host:port for socat target
	proxyHost, proxyPort := splitHostPort(proxyAddr)

	// The gateway needs to reach the Cordon server on the host.
	// Create a non-internal "bridge" network for the gateway's external leg.
	extNetName := networkName + gatewayNetSuffix
	extNetID, err := b.client.NetworkCreate(ctx, extNetName, network.CreateOptions{
		Driver: "bridge",
		Labels: mergeLabels(labels, map[string]string{
			gatewayLabelKey: gatewayLabelVal,
		}),
	})
	if err != nil {
		return "", fmt.Errorf("creating gateway external network: %w", err)
	}

	gwLabels := mergeLabels(labels, map[string]string{
		gatewayLabelKey:          gatewayLabelVal,
		"cordon.gateway-for-net": networkName,
	})

	// socat listens on all interfaces inside the gateway and forwards to the proxy.
	socatCmd := fmt.Sprintf(
		"socat TCP-LISTEN:%s,fork,reuseaddr TCP:%s:%s",
		gatewayPort, proxyHost, proxyPort,
	)

	resp, err := b.client.ContainerCreate(ctx,
		&container.Config{
			Image:  gatewayImage,
			Labels: gwLabels,
			Cmd:    []string{"sh", "-c", "apk add --no-cache socat > /dev/null 2>&1 && " + socatCmd},
		},
		&container.HostConfig{
			NetworkMode: container.NetworkMode(extNetName),
			Resources: container.Resources{
				CPUQuota: 50000,  // 0.5 CPU — gateway is lightweight
				Memory:   64 << 20, // 64 MB
			},
			ExtraHosts: []string{"host.docker.internal:host-gateway"},
		},
		nil, nil, gwName,
	)
	if err != nil {
		b.client.NetworkRemove(ctx, extNetID.ID)
		return "", fmt.Errorf("creating gateway container: %w", err)
	}

	// Connect gateway to the internal workspace network (second NIC)
	if err := b.client.NetworkConnect(ctx, networkName, resp.ID, nil); err != nil {
		b.client.ContainerRemove(ctx, resp.ID, container.RemoveOptions{Force: true})
		b.client.NetworkRemove(ctx, extNetID.ID)
		return "", fmt.Errorf("connecting gateway to workspace network: %w", err)
	}

	if err := b.client.ContainerStart(ctx, resp.ID, container.StartOptions{}); err != nil {
		b.client.ContainerRemove(ctx, resp.ID, container.RemoveOptions{Force: true})
		b.client.NetworkRemove(ctx, extNetID.ID)
		return "", fmt.Errorf("starting gateway container: %w", err)
	}

	// Read the gateway's IP on the internal workspace network
	internalIP, err := b.containerIPOnNetwork(ctx, resp.ID, networkName)
	if err != nil {
		b.client.ContainerRemove(ctx, resp.ID, container.RemoveOptions{Force: true})
		b.client.NetworkRemove(ctx, extNetID.ID)
		return "", fmt.Errorf("reading gateway IP: %w", err)
	}

	internalAddr := fmt.Sprintf("%s:%s", internalIP, gatewayPort)
	log.Printf("[docker] gateway %s ready, workspace proxy addr: %s", gwName, internalAddr)
	return internalAddr, nil
}

// RemoveProxyAccess tears down the gateway container and its external network.
func (b *Backend) RemoveProxyAccess(ctx context.Context, networkName string) error {
	gwName := gatewayName(networkName)
	extNetName := networkName + gatewayNetSuffix

	// Remove gateway container
	if err := b.client.ContainerRemove(ctx, gwName, container.RemoveOptions{Force: true}); err != nil {
		log.Printf("[docker] warning: gateway container remove failed: %v", err)
	} else {
		log.Printf("[docker] removed gateway container %s", gwName)
	}

	// Remove external network
	if err := b.client.NetworkRemove(ctx, extNetName); err != nil {
		log.Printf("[docker] warning: gateway network remove failed: %v", err)
	}

	return nil
}

// containerIPOnNetwork inspects a container and returns its IP on the given network.
func (b *Backend) containerIPOnNetwork(ctx context.Context, containerID, networkName string) (string, error) {
	inspect, err := b.client.ContainerInspect(ctx, containerID)
	if err != nil {
		return "", err
	}
	for name, netSettings := range inspect.NetworkSettings.Networks {
		if name == networkName {
			return netSettings.IPAddress, nil
		}
	}
	return "", fmt.Errorf("container %s not connected to network %s", containerID[:12], networkName)
}

// pullIfMissing pulls an image only if it's not already available locally.
func (b *Backend) pullIfMissing(ctx context.Context, image string) error {
	_, _, err := b.client.ImageInspectWithRaw(ctx, image)
	if err == nil {
		return nil // already present
	}
	reader, err := b.client.ImagePull(ctx, image, dockerimage.PullOptions{})
	if err != nil {
		return err
	}
	_, _ = io.Copy(io.Discard, reader)
	reader.Close()
	return nil
}

func gatewayName(networkName string) string {
	return networkName + "-gateway"
}

func splitHostPort(addr string) (string, string) {
	i := strings.LastIndex(addr, ":")
	if i < 0 {
		return addr, gatewayPort
	}
	return addr[:i], addr[i+1:]
}

func mergeLabels(base, extra map[string]string) map[string]string {
	merged := make(map[string]string, len(base)+len(extra))
	for k, v := range base {
		merged[k] = v
	}
	for k, v := range extra {
		merged[k] = v
	}
	return merged
}
