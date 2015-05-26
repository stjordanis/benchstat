// Copyright 2015 The Go Authors.  All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Benchstat computes and compares statistics about benchmarks.
//
// Usage:
//
//	benchstat [-delta-test name] [-html] old.txt [new.txt] [more.txt ...]
//
// Each input file should contain the concatenated output of a number
// of runs of ``go test -bench.'' For each different benchmark listed in an input file,
// benchstat computes the mean, minimum, and maximum run time,
// after removing outliers using the interquartile range rule.
//
// If invoked on a single input file, benchstat prints the per-benchmark statistics
// for that file.
//
// If invoked on a pair of input files, benchstat adds to the output a column
// showing the statistics from the second file and a column showing the
// percent change in mean from the first to the second file.
// Next to the percent change, benchstat shows the p-value and sample
// sizes from a test of the two distributions of benchmark times.
// Small p-values indicate that the two distributions are significantly different.
// If the test indicates that there was no significant change between the two
// benchmarks (defined as p > 0.05), benchstat displays a single ~ instead of
// the percent change.
//
// The -delta-test option controls which significance test is applied:
// utest (Mann-Whitney U-test), ttest (two-sample Welch t-test), or none.
// The default is the U-test, sometimes also referred to as the Wilcoxon rank
// sum test.
//
// If invoked on more than two input files, benchstat prints the per-benchmark
// statistics for all the files, showing one column of statistics for each file,
// with no column for percent change or statistical significance.
//
// The -html option causes benchstat to print the results as an HTML table.
//
// Example
//
// Suppose we collect benchmark results from running ``go test -bench=Encode''
// five times before and after a particular change.
//
// The file old.txt contains:
//
//	BenchmarkGobEncode   	100	  13552735 ns/op	  56.63 MB/s
//	BenchmarkJSONEncode  	 50	  32395067 ns/op	  59.90 MB/s
//	BenchmarkGobEncode   	100	  13553943 ns/op	  56.63 MB/s
//	BenchmarkJSONEncode  	 50	  32334214 ns/op	  60.01 MB/s
//	BenchmarkGobEncode   	100	  13606356 ns/op	  56.41 MB/s
//	BenchmarkJSONEncode  	 50	  31992891 ns/op	  60.65 MB/s
//	BenchmarkGobEncode   	100	  13683198 ns/op	  56.09 MB/s
//	BenchmarkJSONEncode  	 50	  31735022 ns/op	  61.15 MB/s
//
// The file new.txt contains:
//
//	BenchmarkGobEncode   	 100	  11773189 ns/op	  65.19 MB/s
//	BenchmarkJSONEncode  	  50	  32036529 ns/op	  60.57 MB/s
//	BenchmarkGobEncode   	 100	  11942588 ns/op	  64.27 MB/s
//	BenchmarkJSONEncode  	  50	  32156552 ns/op	  60.34 MB/s
//	BenchmarkGobEncode   	 100	  11786159 ns/op	  65.12 MB/s
//	BenchmarkJSONEncode  	  50	  31288355 ns/op	  62.02 MB/s
//	BenchmarkGobEncode   	 100	  11628583 ns/op	  66.00 MB/s
//	BenchmarkJSONEncode  	  50	  31559706 ns/op	  61.49 MB/s
//	BenchmarkGobEncode   	 100	  11815924 ns/op	  64.96 MB/s
//	BenchmarkJSONEncode  	  50	  31765634 ns/op	  61.09 MB/s
//
// The order of the lines in the file does not matter, except that the
// output lists benchmarks in order of appearance.
//
// If run with just one input file, benchstat summarizes that file:
//
//	$ benchstat old.txt
//	name        time/op
//	GobEncode   13.6ms ± 1%
//	JSONEncode  32.1ms ± 1%
//	$
//
// If run with two input files, benchstat summarizes and compares:
//
//	$ benchstat old.txt new.txt
//	name        old time/op  new time/op  delta
//	GobEncode   13.6ms ± 1%  11.8ms ± 1%  -13.31% (p=0.016 n=4+5)
//	JSONEncode  32.1ms ± 1%  31.8ms ± 1%     ~    (p=0.286 n=4+5)
//	$
//
// Note that the JSONEncode result is reported as
// statistically insignificant instead of a -0.93% delta.
//
package main

import (
	"bytes"
	"flag"
	"fmt"
	"html"
	"io/ioutil"
	"log"
	"os"
	"strconv"
	"strings"
	"unicode/utf8"

	"rsc.io/benchstat/internal/stats"
)

func usage() {
	fmt.Fprintf(os.Stderr, "usage: benchstat [options] old.txt [new.txt] [more.txt ...]\n")
	fmt.Fprintf(os.Stderr, "options:\n")
	flag.PrintDefaults()
	os.Exit(2)
}

var (
	flagDeltaTest = flag.String("delta-test", "utest", "significance `test` to apply to delta: utest, ttest, or none")
	flagHTML      = flag.Bool("html", false, "print results as an HTML table")
)

var deltaTestNames = map[string]func(old, new *Benchmark) (float64, error){
	"none":   notest,
	"u":      utest,
	"u-test": utest,
	"utest":  utest,
	"t":      ttest,
	"t-test": ttest,
	"ttest":  ttest,
}

func main() {
	log.SetPrefix("benchstats: ")
	log.SetFlags(0)
	flag.Usage = usage
	flag.Parse()
	deltaTest := deltaTestNames[strings.ToLower(*flagDeltaTest)]
	if flag.NArg() < 1 || deltaTest == nil {
		flag.Usage()
	}

	before := readFile(flag.Arg(0))
	var out [][]string
	switch flag.NArg() {
	case 1:
		out = append(out, []string{"name", "time/op"})
		for _, old := range before.Benchmarks {
			out = append(out, []string{old.Name, old.Time(old.Scaler())})
		}
	case 2:
		after := readFile(flag.Arg(1))
		out = append(out, []string{"name", "old time/op", "new time/op", "delta"})
		for _, old := range before.Benchmarks {
			new := after.ByName[old.Name]
			if new == nil {
				continue
			}

			pval, testerr := deltaTest(old, new)

			scaler := old.Scaler()
			row := []string{old.Name, old.Time(scaler), new.Time(scaler), "~   "}
			if testerr == stats.ErrZeroVariance {
				row[3] = "zero variance"
			} else if testerr == stats.ErrSampleSize {
				row[3] = "too few samples"
			} else if testerr != nil {
				row[3] = fmt.Sprintf("(%s)", testerr)
			} else if pval <= 0.05 {
				row[3] = fmt.Sprintf("%+.2f%%", ((new.Mean/old.Mean)-1.0)*100.0)
			}
			if testerr == nil && pval != -1 {
				row[3] += fmt.Sprintf(" (p=%0.3f n=%d+%d)", pval, len(old.RTimes), len(new.RTimes))
			}
			out = append(out, row)
		}
	default:
		groups := []*Group{before}
		hdr := []string{"benchmark", before.File}
		for _, file := range flag.Args()[1:] {
			group := readFile(file)
			groups = append(groups, group)
			hdr = append(hdr, group.File)
		}
		out = append(out, hdr)

		done := map[string]bool{}
		for _, group := range groups {
			for _, bench := range group.Benchmarks {
				name := bench.Name
				if done[name] {
					continue
				}
				done[name] = true
				row := []string{name}
				scaler := bench.Scaler()
				for _, group := range groups {
					b := group.ByName[name]
					if b == nil {
						row = append(row, "")
						continue
					}
					row = append(row, b.Time(scaler))
				}
				for row[len(row)-1] == "" {
					row = row[:len(row)-1]
				}
				out = append(out, row)
			}
		}
	}

	numColumn := 0
	for _, row := range out {
		if numColumn < len(row) {
			numColumn = len(row)
		}
	}

	if *flagHTML {
		var buf bytes.Buffer
		fmt.Fprintf(&buf, "<style>.benchstat tbody td:nth-child(1n+2) { text-align: right; padding: 0em 1em; }</style>\n")
		fmt.Fprintf(&buf, "<table class='benchstat'>\n")
		printRow := func(row []string, tag string) {
			fmt.Fprintf(&buf, "<tr>")
			for _, cell := range row {
				fmt.Fprintf(&buf, "<%s>%s</%s>", tag, html.EscapeString(cell), tag)
			}
			fmt.Fprintf(&buf, "\n")
		}
		printRow(out[0], "th")
		for _, row := range out[1:] {
			printRow(row, "td")
		}
		fmt.Fprintf(&buf, "</table>\n")
		os.Stdout.Write(buf.Bytes())
		return
	}

	max := make([]int, numColumn)
	for _, row := range out {
		for i, s := range row {
			n := utf8.RuneCountInString(s)
			if max[i] < n {
				max[i] = n
			}
		}
	}

	var buf bytes.Buffer
	// headings
	row := out[0]
	for i, s := range row {
		switch i {
		case 0:
			fmt.Fprintf(&buf, "%-*s", max[i], s)
		default:
			fmt.Fprintf(&buf, "  %-*s", max[i], s)
		case len(row) - 1:
			fmt.Fprintf(&buf, "  %s\n", s)
		}
	}

	// data
	for _, row := range out[1:] {
		for i, s := range row {
			switch i {
			case 0:
				fmt.Fprintf(&buf, "%-*s", max[i], s)
			default:
				fmt.Fprintf(&buf, "  %*s", max[i], s)
			}
		}
		fmt.Fprintf(&buf, "\n")
	}

	os.Stdout.Write(buf.Bytes())
}

func (b *Benchmark) Scaler() func(*Benchmark) string {
	var format string
	var scale float64
	switch x := b.Mean / 1e9; {
	case x >= 99.5:
		format, scale = "%.0fs", 1
	case x >= 9.95:
		format, scale = "%.1fs", 1
	case x >= 0.995:
		format, scale = "%.2fs", 1
	case x >= 0.0995:
		format, scale = "%.0fms", 1000
	case x >= 0.00995:
		format, scale = "%.1fms", 1000
	case x >= 0.000995:
		format, scale = "%.2fms", 1000
	case x >= 0.0000995:
		format, scale = "%.0fµs", 1000*1000
	case x >= 0.00000995:
		format, scale = "%.1fµs", 1000*1000
	case x >= 0.000000995:
		format, scale = "%.2fµs", 1000*1000
	case x >= 0.0000000995:
		format, scale = "%.0fns", 1000*1000*1000
	case x >= 0.00000000995:
		format, scale = "%.1fns", 1000*1000*1000
	default:
		format, scale = "%.2fns", 1000*1000*1000
	}
	return func(b *Benchmark) string {
		return fmt.Sprintf(format, b.Mean/1e9*scale)
	}
}

func (b *Benchmark) Time(scaler func(*Benchmark) string) string {
	diff := 1 - b.Min/b.Mean
	if d := b.Max/b.Mean - 1; d > diff {
		diff = d
	}
	return fmt.Sprintf("%s ±%3s", scaler(b), fmt.Sprintf("%.0f%%", diff*100.0))
}

// A Group is a collection of benchmark results for a specific version of the code.
type Group struct {
	File       string
	Benchmarks []*Benchmark
	ByName     map[string]*Benchmark
}

// A Benchmark is a collection of runs of a specific benchmark.
type Benchmark struct {
	Name   string
	Runs   []Run
	Times  []float64 // Times of runs in nanoseconds
	RTimes []float64 // Times with outliers removed
	Min    float64   // minimum run
	Mean   float64
	Max    float64 // maximum run
}

// A Run records the result of a single benchmark run.
type Run struct {
	N  int
	Ns float64
}

// readFile reads a benchmark group from a file.
func readFile(file string) *Group {
	g := &Group{File: file, ByName: make(map[string]*Benchmark)}

	text, err := ioutil.ReadFile(file)
	if err != nil {
		log.Fatal(err)
	}
	for _, line := range strings.Split(string(text), "\n") {
		f := strings.Fields(line)
		if len(f) < 4 {
			continue
		}
		name := f[0]
		if !strings.HasPrefix(name, "Benchmark") {
			continue
		}
		name = strings.TrimPrefix(name, "Benchmark")
		n, _ := strconv.Atoi(f[1])
		var ns float64
		for i := 2; i+2 <= len(f); i += 2 {
			if f[i+1] == "ns/op" {
				ns, _ = strconv.ParseFloat(f[i], 64)
				break
			}
		}
		if n == 0 || ns == 0 {
			continue
		}
		b := g.ByName[name]
		if b == nil {
			b = &Benchmark{Name: name}
			g.ByName[name] = b
			g.Benchmarks = append(g.Benchmarks, b)
		}
		b.Runs = append(b.Runs, Run{n, ns})
		b.Times = append(b.Times, ns)
	}

	for _, b := range g.Benchmarks {
		// Discard outliers.
		times := stats.Sample{Xs: b.Times}
		q1, q3 := times.Percentile(0.25), times.Percentile(0.75)
		lo, hi := q1-1.5*(q3-q1), q3+1.5*(q3-q1)
		for _, time := range b.Times {
			if lo <= time && time <= hi {
				b.RTimes = append(b.RTimes, time)
			}
		}

		// Compute statistics of remaining data.
		b.Min, b.Max = stats.Bounds(b.RTimes)
		b.Mean = stats.Mean(b.RTimes)
	}

	return g
}

// Significance tests.

func notest(old, new *Benchmark) (pval float64, err error) {
	return -1, nil
}

func ttest(old, new *Benchmark) (pval float64, err error) {
	t, err := stats.TwoSampleWelchTTest(stats.Sample{Xs: old.RTimes}, stats.Sample{Xs: new.RTimes}, stats.LocationDiffers)
	if err != nil {
		return -1, err
	}
	return t.P, nil
}

func utest(old, new *Benchmark) (pval float64, err error) {
	u, err := stats.MannWhitneyUTest(old.RTimes, new.RTimes, stats.LocationDiffers)
	if err != nil {
		return -1, err
	}
	return u.P, nil
}
