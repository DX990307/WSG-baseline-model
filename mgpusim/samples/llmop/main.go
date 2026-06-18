package main

import (
	"flag"

	"github.com/sarchlab/mgpusim/v3/benchmarks/LLMbenchmarks/llmop"
	"github.com/sarchlab/mgpusim/v3/samples/runner"
)

func main() {
	flag.Parse()

	runner := new(runner.Runner).ParseFlag().Init()

	benchmark := llmop.NewBenchmarkFromFlags(runner.Driver())
	runner.AddBenchmark(benchmark)
	runner.Run()
}
