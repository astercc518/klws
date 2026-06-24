// Package deploy contains parse-and-assert tests for k8s manifests in deploy/.
// No kubectl is required — manifests are parsed as plain YAML maps.
package deploy

import (
	"os"
	"path/filepath"
	"testing"

	"gopkg.in/yaml.v3"
)

// loadYAML reads a YAML file relative to this test file's location and
// unmarshals it into a generic map.
func loadYAML(t *testing.T, rel string) map[string]any {
	t.Helper()
	path := filepath.Join("../../deploy", rel)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var out map[string]any
	if err := yaml.Unmarshal(data, &out); err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	return out
}

// dig walks a nested map[string]any using dot-separated keys and returns the
// final value or nil if the path is absent.
func dig(m map[string]any, keys ...string) any {
	var cur any = m
	for _, k := range keys {
		mm, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur = mm[k]
	}
	return cur
}

func intVal(v any) int {
	switch x := v.(type) {
	case int:
		return x
	case uint64:
		return int(x)
	case int64:
		return int(x)
	case float64:
		return int(x)
	}
	return -1
}

func TestDeploymentManifest(t *testing.T) {
	m := loadYAML(t, "deployment.yaml")

	spec := func(keys ...string) any {
		path := append([]string{"spec"}, keys...)
		return dig(m, path...)
	}

	t.Run("rollingUpdate maxUnavailable is 0", func(t *testing.T) {
		v := spec("strategy", "rollingUpdate", "maxUnavailable")
		if intVal(v) != 0 {
			t.Fatalf("maxUnavailable = %v, want 0", v)
		}
	})

	t.Run("rollingUpdate maxSurge is 1", func(t *testing.T) {
		v := spec("strategy", "rollingUpdate", "maxSurge")
		if intVal(v) != 1 {
			t.Fatalf("maxSurge = %v, want 1", v)
		}
	})

	t.Run("terminationGracePeriodSeconds >= 35", func(t *testing.T) {
		// PRESTOP(5) + SHUTDOWN(30) = 35; manifest sets 45.
		v := dig(m, "spec", "template", "spec", "terminationGracePeriodSeconds")
		if intVal(v) < 35 {
			t.Fatalf("terminationGracePeriodSeconds = %v, want >= 35", v)
		}
	})

	// Navigate to the first container.
	containers := func() []any {
		cs := dig(m, "spec", "template", "spec", "containers")
		if s, ok := cs.([]any); ok {
			return s
		}
		return nil
	}
	firstContainer := func() map[string]any {
		cs := containers()
		if len(cs) == 0 {
			return nil
		}
		mm, _ := cs[0].(map[string]any)
		return mm
	}

	t.Run("readinessProbe path is /readyz", func(t *testing.T) {
		c := firstContainer()
		path := dig(c, "readinessProbe", "httpGet", "path")
		if path != "/readyz" {
			t.Fatalf("readinessProbe.httpGet.path = %v, want /readyz", path)
		}
	})

	t.Run("livenessProbe path is /healthz", func(t *testing.T) {
		c := firstContainer()
		path := dig(c, "livenessProbe", "httpGet", "path")
		if path != "/healthz" {
			t.Fatalf("livenessProbe.httpGet.path = %v, want /healthz", path)
		}
	})

	t.Run("lifecycle preStop present", func(t *testing.T) {
		c := firstContainer()
		preStop := dig(c, "lifecycle", "preStop")
		if preStop == nil {
			t.Fatal("lifecycle.preStop is absent")
		}
	})
}

func TestPDBManifest(t *testing.T) {
	m := loadYAML(t, "pdb.yaml")

	t.Run("kind is PodDisruptionBudget", func(t *testing.T) {
		if m["kind"] != "PodDisruptionBudget" {
			t.Fatalf("kind = %v, want PodDisruptionBudget", m["kind"])
		}
	})

	t.Run("minAvailable is 1", func(t *testing.T) {
		v := dig(m, "spec", "minAvailable")
		if intVal(v) != 1 {
			t.Fatalf("minAvailable = %v, want 1", v)
		}
	})
}
