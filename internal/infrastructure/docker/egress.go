package docker

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"time"

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
//	[workspace container] --(internal network)--> [gateway/haproxy] --(bridge network)--> [Cordon proxy]
//
// The gateway runs haproxy with PROXY protocol v1 so that the Cordon server
// sees the real workspace container IP, not the Docker NAT address. This is
// critical on macOS Docker Desktop where all container→host traffic appears
// from 127.0.0.1 due to the VM NAT layer.
func (b *Backend) EnsureProxyAccess(ctx context.Context, networkName string, proxyAddr string, labels map[string]string) (string, error) {
	gwName := gatewayName(networkName)

	slog.Info("ensuring proxy access", "component", "docker", "gateway", gwName, "proxy", proxyAddr)

	// Pull the lightweight gateway image
	if err := b.pullIfMissing(ctx, gatewayImage); err != nil {
		return "", fmt.Errorf("pulling gateway image: %w", err)
	}

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

	// HAProxy config: forward TCP with PROXY protocol v1 so the Cordon server
	// sees the real workspace container IP, not the Docker NAT address.
	haproxyCfg := fmt.Sprintf(`defaults
    mode tcp
    timeout connect 10s
    timeout client 1h
    timeout server 1h

frontend workspace_proxy
    bind *:%s
    default_backend cordon_proxy

backend cordon_proxy
    server cordon %s send-proxy
`, gatewayPort, proxyAddr)

	gwCmd := fmt.Sprintf(`apk add --no-cache haproxy > /dev/null 2>&1 && mkdir -p /etc/haproxy && cat > /etc/haproxy/haproxy.cfg << 'HAPCFG'
%sHAPCFG
exec haproxy -f /etc/haproxy/haproxy.cfg -db`, haproxyCfg)

	resp, err := b.client.ContainerCreate(ctx,
		&container.Config{
			Image:  gatewayImage,
			Labels: gwLabels,
			Cmd:    []string{"sh", "-c", gwCmd},
		},
		&container.HostConfig{
			NetworkMode: container.NetworkMode(extNetName),
			Resources: container.Resources{
				CPUQuota: 50000,    // 0.5 CPU — gateway is lightweight
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

	// Read the gateway's IP on the internal workspace network (used by workspace containers)
	internalIP, err := b.containerIPOnNetwork(ctx, resp.ID, networkName)
	if err != nil {
		b.client.ContainerRemove(ctx, resp.ID, container.RemoveOptions{Force: true})
		b.client.NetworkRemove(ctx, extNetID.ID)
		return "", fmt.Errorf("reading gateway internal IP: %w", err)
	}

	internalAddr := fmt.Sprintf("%s:%s", internalIP, gatewayPort)

	// Wait for haproxy to start listening. The gateway runs apk add + haproxy,
	// which takes a few seconds. Without this, workspace containers may try
	// to connect before the gateway is ready.
	if err := b.waitForGatewayReady(ctx, resp.ID, 60*time.Second); err != nil {
		slog.Warn("gateway readiness check failed", "component", "docker", "error", err)
	}

	slog.Info("gateway ready", "component", "docker", "gateway", gwName, "workspace_proxy_addr", internalAddr)
	return internalAddr, nil
}

// RemoveProxyAccess tears down the gateway container and its external network.
func (b *Backend) RemoveProxyAccess(ctx context.Context, networkName string) error {
	gwName := gatewayName(networkName)
	extNetName := networkName + gatewayNetSuffix

	// Remove gateway container
	if err := b.client.ContainerRemove(ctx, gwName, container.RemoveOptions{Force: true}); err != nil {
		slog.Warn("gateway container remove failed", "component", "docker", "error", err)
	} else {
		slog.Info("removed gateway container", "component", "docker", "gateway", gwName)
	}

	// Remove external network
	if err := b.client.NetworkRemove(ctx, extNetName); err != nil {
		slog.Warn("gateway network remove failed", "component", "docker", "error", err)
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

// waitForGatewayReady polls from inside the gateway container until haproxy is
// listening. We can't check from the host because the internal network IP is
// unreachable from outside Docker.
func (b *Backend) waitForGatewayReady(ctx context.Context, containerID string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	checkCmd := fmt.Sprintf(`nc -z 127.0.0.1 %s`, gatewayPort)
	for time.Now().Before(deadline) {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		execCfg := container.ExecOptions{
			Cmd: []string{"sh", "-c", checkCmd},
		}
		execID, err := b.client.ContainerExecCreate(ctx, containerID, execCfg)
		if err != nil {
			time.Sleep(1 * time.Second)
			continue
		}
		if err := b.client.ContainerExecStart(ctx, execID.ID, container.ExecStartOptions{}); err != nil {
			time.Sleep(1 * time.Second)
			continue
		}
		inspect, err := b.client.ContainerExecInspect(ctx, execID.ID)
		if err == nil && inspect.ExitCode == 0 {
			return nil
		}
		time.Sleep(1 * time.Second)
	}
	return fmt.Errorf("timeout waiting for gateway haproxy (container=%s)", containerID[:12])
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
