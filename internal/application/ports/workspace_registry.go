package ports

import "github.com/google/uuid"

// WorkspaceRegistry tracks workspace network identity so that incoming proxy
// connections can be attributed to the originating workspace. The orchestrator
// registers entries when workspaces are created and removes them on destroy.
//
// This abstraction is backend-agnostic: Docker tracks gateway IPs, Azure might
// use VNet source ranges, etc. The proxy layer only calls Lookup.
type WorkspaceRegistry interface {
	// Register associates a network address (IP or CIDR) with a workspace.
	Register(addr string, workspaceID uuid.UUID)
	// Unregister removes a mapping by address.
	Unregister(addr string)
	// Lookup returns the workspace ID for a network address, or uuid.Nil.
	Lookup(remoteIP string) uuid.UUID
}
