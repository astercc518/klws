package cluster

import (
	"reflect"
	"testing"
)

func TestEvoCluster_ForAndNodes(t *testing.T) {
	c := NewEvoCluster(map[string]string{
		"nodeA": "http://a:8080",
		"nodeB": "http://b:8080",
	}, "key")

	if got := c.Nodes(); !reflect.DeepEqual(got, []string{"nodeA", "nodeB"}) {
		t.Fatalf("Nodes()=%v want sorted [nodeA nodeB]", got)
	}
	cl, ok := c.For("nodeA")
	if !ok || cl == nil {
		t.Fatal("For(nodeA) must resolve")
	}
	if _, ok := c.For("ghost"); ok {
		t.Fatal("For(unknown) must be ok=false")
	}
}

func TestEvoCluster_Empty(t *testing.T) {
	c := NewEvoCluster(nil, "k")
	if len(c.Nodes()) != 0 {
		t.Fatal("empty cluster has no nodes")
	}
	if _, ok := c.For("x"); ok {
		t.Fatal("empty cluster resolves nothing")
	}
}
