package domain

// EgressPolicy defines network-level egress restrictions for a workspace.
// When enabled, the workspace can only reach the Cordon proxy — all other
// outbound traffic is blocked at the network layer, outside the container's
// control.
type EgressPolicy struct {
	Enabled   bool   // whether network-level enforcement is active
	ProxyAddr string // external address of the Cordon proxy (host:port)
}
