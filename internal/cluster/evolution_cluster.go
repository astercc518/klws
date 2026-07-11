package cluster

import "sort"

// EvoCluster is a registry of per-node Evolution HTTP clients. An instance's
// evo_node (account_instances) selects which client its lifecycle/send calls
// go to. DORMANT: composed into the live path only at E6 cutover.
type EvoCluster struct {
	clients map[string]*EvoClient
}

// NewEvoCluster builds one EvoClient per node from a node→baseURL map. All nodes
// share the same apiKey (Evolution's global key).
func NewEvoCluster(baseURLs map[string]string, apiKey string) *EvoCluster {
	c := &EvoCluster{clients: map[string]*EvoClient{}}
	for node, url := range baseURLs {
		c.clients[node] = NewEvoClient(url, apiKey)
	}
	return c
}

// For returns the client for a node, or (nil,false) if the node is unknown.
func (c *EvoCluster) For(node string) (*EvoClient, bool) {
	cl, ok := c.clients[node]
	return cl, ok
}

// Nodes returns the configured node names, sorted (stable).
func (c *EvoCluster) Nodes() []string {
	out := make([]string, 0, len(c.clients))
	for n := range c.clients {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}
