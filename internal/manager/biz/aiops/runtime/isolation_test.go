package runtime_test

import (
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	v1 "github.com/ongridio/ongrid/internal/manager/biz/aiops/runtime/v1/graph/callbacks"
	v2 "github.com/ongridio/ongrid/internal/manager/biz/aiops/runtime/v2/graph/callbacks"
	v3 "github.com/ongridio/ongrid/internal/manager/biz/aiops/runtime/v3/graph/callbacks"
	"github.com/prometheus/client_golang/prometheus"
)

// Versioned policies must never silently delegate to another generation.
func TestRuntimeVersionsDoNotImportEachOther(t *testing.T) {
	const base = "github.com/ongridio/ongrid/internal/manager/biz/aiops/"
	for _, version := range []string{"v1", "v2", "v3", "contract"} {
		t.Run(version, func(t *testing.T) {
			err := filepath.WalkDir(version, func(path string, entry fs.DirEntry, err error) error {
				if err != nil {
					return err
				}
				if entry.IsDir() || !strings.HasSuffix(path, ".go") {
					return nil
				}
				file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
				if err != nil {
					return err
				}
				for _, spec := range file.Imports {
					imported, err := strconv.Unquote(spec.Path.Value)
					if err != nil {
						return err
					}
					if strings.HasPrefix(imported, base+"runtime/") && imported != base+"runtime/contract" && !strings.HasPrefix(imported, base+"runtime/"+version+"/") {
						t.Errorf("%s imports another runtime: %s", path, imported)
					}
					if imported == base+"graph" || strings.HasPrefix(imported, base+"graph/") || imported == base+"chatruntime" {
						t.Errorf("%s imports an unversioned runtime: %s", path, imported)
					}
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}

// All versions may live in one process without duplicate collector panics.
func TestRuntimeVersionsShareMetricsRegistry(t *testing.T) {
	reg := prometheus.NewRegistry()
	for i := 0; i < 2; i++ {
		if v1.NewMetricsHandler(v1.MetricsDeps{Registerer: reg}) == nil ||
			v2.NewMetricsHandler(v2.MetricsDeps{Registerer: reg}) == nil ||
			v3.NewMetricsHandler(v3.MetricsDeps{Registerer: reg}) == nil {
			t.Fatal("missing metrics handler")
		}
	}
	if _, err := reg.Gather(); err != nil {
		t.Fatal(err)
	}
}
