package handler

import (
	"os"
	"sort"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestOpenAPIPathsMatchCurrentAPIRoutes(t *testing.T) {
	specRoutes := readOpenAPIRoutes(t)
	handlerRoutes := map[string]bool{}
	for _, route := range currentAPIRoutes() {
		handlerRoutes[route.Method+" "+strings.TrimPrefix(route.Path, "/api/v1")] = true
	}

	var missing []string
	for route := range handlerRoutes {
		if !specRoutes[route] {
			missing = append(missing, route)
		}
	}
	var extra []string
	for route := range specRoutes {
		if !handlerRoutes[route] {
			extra = append(extra, route)
		}
	}
	if len(missing) > 0 || len(extra) > 0 {
		sort.Strings(missing)
		sort.Strings(extra)
		t.Fatalf("OpenAPI route drift\nmissing from spec: %v\nextra in spec: %v", missing, extra)
	}
}

func readOpenAPIRoutes(t *testing.T) map[string]bool {
	t.Helper()
	raw, err := os.ReadFile("../../spec/openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var spec struct {
		Paths map[string]map[string]any `yaml:"paths"`
	}
	if err := yaml.Unmarshal(raw, &spec); err != nil {
		t.Fatal(err)
	}

	routes := map[string]bool{}
	for path, item := range spec.Paths {
		for method := range item {
			method = strings.ToUpper(method)
			switch method {
			case "GET", "POST", "PUT", "PATCH", "DELETE":
				routes[method+" "+path] = true
			}
		}
	}
	return routes
}
