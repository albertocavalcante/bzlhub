package farol

import (
	"os"
	"runtime/debug"
)

// readBuildVersion returns the VCS revision from build info, or
// "(devel)" if unavailable.
func readBuildVersion() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "(devel)"
	}
	// Prefer the explicit module version if set (e.g. installed via go install).
	if info.Main.Version != "" && info.Main.Version != "(devel)" {
		return info.Main.Version
	}
	// Fall back to VCS revision (set when built with `go build` against a repo).
	for _, s := range info.Settings {
		if s.Key == "vcs.revision" && s.Value != "" {
			if len(s.Value) > 12 {
				return s.Value[:12]
			}
			return s.Value
		}
	}
	return "(devel)"
}

// detectHostAttrs returns OTel resource attributes derived from the host.
// Returns a map so it can be merged into c.ResourceAttributes by Setup.
func detectHostAttrs() map[string]string {
	out := map[string]string{}
	if h, err := os.Hostname(); err == nil && h != "" {
		out["host.name"] = h
	}
	return out
}

// detectK8sAttrs returns OTel resource attributes derived from
// Kubernetes downward-API env vars. Returns empty map if not in k8s.
//
// Set these in the pod spec for them to populate:
//
//	env:
//	  - name: KUBERNETES_NAMESPACE
//	    valueFrom: { fieldRef: { fieldPath: metadata.namespace } }
//	  - name: KUBERNETES_POD_NAME
//	    valueFrom: { fieldRef: { fieldPath: metadata.name } }
//	  - name: KUBERNETES_NODE_NAME
//	    valueFrom: { fieldRef: { fieldPath: spec.nodeName } }
func detectK8sAttrs() map[string]string {
	out := map[string]string{}
	for envKey, otelKey := range map[string]string{
		"KUBERNETES_NAMESPACE": "k8s.namespace.name",
		"KUBERNETES_POD_NAME":  "k8s.pod.name",
		"KUBERNETES_NODE_NAME": "k8s.node.name",
	} {
		if v := os.Getenv(envKey); v != "" {
			out[otelKey] = v
		}
	}
	return out
}

// mergeAttrs merges src into dst (mutating dst). Keys in dst win.
func mergeAttrs(dst, src map[string]string) {
	for k, v := range src {
		if _, exists := dst[k]; !exists {
			dst[k] = v
		}
	}
}
