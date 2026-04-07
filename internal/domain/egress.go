package domain

// EgressPolicy defines network-level egress restrictions for workspaces.
// When enabled, workspace containers can only reach the Cordon proxy — all
// other outbound traffic is blocked at the network layer, outside the
// container's control.
type EgressPolicy struct {
	Enabled   bool     // whether network-level enforcement is active
	ProxyAddr string   // external address of the Cordon proxy (host:port)
	Allowlist []string // global default egress allowlist
}

// WorkspaceEgressPolicy extends the global policy for a specific workspace.
// The effective allowlist is the union of the global allowlist and the
// workspace-specific entries.
type WorkspaceEgressPolicy struct {
	AdditionalHosts []string `json:"additional_hosts,omitempty"`
}
