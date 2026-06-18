package main

import (
	"flag"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	runnerpkg "github.com/sarchlab/akkalat/400latency/runner"
	firbench "github.com/sarchlab/mgpusim/v3/benchmarks/heteromark/fir"
)

func Test400Latency(t *testing.T) {
	metricStem := filepath.Join(t.TempDir(), "test400latency_metrics")

	cmd := exec.Command(os.Args[0], "-test.run=Test400LatencyHelperProcess")
	cmd.Env = append(os.Environ(),
		"GO_WANT_HELPER_PROCESS=1",
		"TEST400LATENCY_METRIC_STEM="+metricStem,
	)

	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("400latency helper process failed: %v\n%s", err, output)
	}

	metricFile := metricStem + ".csv"
	metricBytes, err := os.ReadFile(metricFile)
	if err != nil {
		t.Fatalf("failed to read generated metric file %q: %v\nhelper output:\n%s",
			metricFile, err, output)
	}

	metricContent := string(metricBytes)
	if !strings.Contains(metricContent, "kernel_time") {
		t.Fatalf("metric file %q does not contain kernel_time:\n%s",
			metricFile, metricContent)
	}

	if !strings.Contains(metricContent, "total_time") {
		t.Fatalf("metric file %q does not contain total_time:\n%s",
			metricFile, metricContent)
	}

	if !strings.Contains(metricContent, "ptcl_mode_enabled") {
		t.Fatalf("metric file %q does not contain ptcl_mode_enabled:\n%s",
			metricFile, metricContent)
	}

	if !strings.Contains(metricContent, "iommu_req_count") {
		t.Fatalf("metric file %q does not contain iommu_req_count:\n%s",
			metricFile, metricContent)
	}

	if !strings.Contains(metricContent, "req_to_mmu_count") {
		t.Fatalf("metric file %q does not contain req_to_mmu_count:\n%s",
			metricFile, metricContent)
	}
}

func Test400LatencyHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_HELPER_PROCESS") != "1" {
		return
	}

	metricStem := os.Getenv("TEST400LATENCY_METRIC_STEM")
	if metricStem == "" {
		t.Fatal("TEST400LATENCY_METRIC_STEM is required")
	}

	if err := flag.Set("metric-file-name", metricStem); err != nil {
		t.Fatalf("failed to set metric-file-name: %v", err)
	}
	if err := flag.Set("num-memory-banks", "16"); err != nil {
		t.Fatalf("failed to set num-memory-banks: %v", err)
	}
	if err := flag.Set("bandwidth", "48"); err != nil {
		t.Fatalf("failed to set bandwidth: %v", err)
	}
	if err := flag.Set("switch-latency", "32"); err != nil {
		t.Fatalf("failed to set switch-latency: %v", err)
	}
	if err := flag.Set("magic-memory-copy", "true"); err != nil {
		t.Fatalf("failed to set magic-memory-copy: %v", err)
	}
	if err := flag.Set("report-all", "true"); err != nil {
		t.Fatalf("failed to set report-all: %v", err)
	}
	if err := flag.Set("gmmu-initial-ptcl-mode", "false"); err != nil {
		t.Fatalf("failed to set gmmu-initial-ptcl-mode: %v", err)
	}
	if err := flag.Set("gmmu-ptcl-threshold-low", "2"); err != nil {
		t.Fatalf("failed to set gmmu-ptcl-threshold-low: %v", err)
	}
	if err := flag.Set("gmmu-ptcl-threshold-high", "6"); err != nil {
		t.Fatalf("failed to set gmmu-ptcl-threshold-high: %v", err)
	}

	runner := (&runnerpkg.Runner{
		Timing:         true,
		DisableServers: true,
	}).Init()

	benchmark := firbench.NewBenchmark(runner.Driver())
	benchmark.Length = 1024

	runner.AddBenchmark(benchmark)
	runner.Run()

	t.Fatal("runner.Run returned unexpectedly")
}
