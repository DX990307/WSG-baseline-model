package main

import (
	"flag"
	"runtime"

	_ "net/http/pprof"

	"github.com/sarchlab/akkalat/2MB/runner"
	"github.com/sarchlab/akkalat/benchmarkselection"
)

var benchmarkFlag = flag.String("benchmark", "fir",
	"Which benchmark to run")

var benchmarksize = flag.Int("benchmark-size", 4096,
	"Which benchmark to run")

func main() {
	flag.Parse()
	runtime.GOMAXPROCS(4)
	// http.ListenAndServe("localhost:6060", nil)
	runner := new(runner.Runner).ParseFlag().Init()

	benchmark := benchmarkselection.SelectBenchmark(
		*benchmarkFlag, runner.Driver())

	runner.AddBenchmark(benchmark)
	runner.Run()
}
