/*
Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package main

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"github.com/aws/aws-sdk-go/aws/session"
	ec22 "github.com/aws/aws-sdk-go/service/ec2"
	"github.com/aws/karpenter/pkg/cloudprovider/aws"
	"go/format"
	"io/ioutil"
	"log"
	"os"
	"runtime"
	"runtime/pprof"
	"sort"
	"strings"
	"time"
)

func main() {
	flag.Parse()
	if flag.NArg() != 1 {
		log.Printf("Usage: %s pkg/cloudprovider/aws/zz_generated.pricing.go", os.Args[0])
		os.Exit(1)
	}

	f, err := os.Create("pricing.heapprofile")
	if err != nil {
		log.Fatal("could not create memory profile: ", err)
	}
	defer f.Close() // error handling omitted for example

	const region = "us-east-1"
	os.Setenv("AWS_SDK_LOAD_CONFIG", "true")
	os.Setenv("AWS_REGION", region)
	ctx := context.Background()
	sess := session.Must(session.NewSession())
	ec2 := ec22.New(sess)
	updateStarted := time.Now()
	pricingProvider := aws.NewPricingProvider(ctx, aws.NewPricingAPI(sess, region), ec2, region, false, make(chan struct{}))

	for {
		if pricingProvider.OnDemandLastUpdated().After(updateStarted) && pricingProvider.SpotLastUpdated().After(updateStarted) {
			break
		}
		log.Println("waiting on pricing update...")
		time.Sleep(1 * time.Second)
	}

	src := &bytes.Buffer{}
	fmt.Fprintln(src, "//go:build !ignore_autogenerated")
	fmt.Fprintln(src, "package aws")
	fmt.Fprintln(src, `import "time"`)
	now := time.Now().UTC().Format(time.RFC3339)
	fmt.Fprintf(src, "// generated at %s for %s\n\n\n", now, region)
	fmt.Fprintf(src, "var initialPriceUpdate, _ = time.Parse(time.RFC3339, \"%s\")\n", now)

	instanceTypes := pricingProvider.InstanceTypes()
	sort.Strings(instanceTypes)

	writePricing(src, instanceTypes, "initialOnDemandPrices", pricingProvider.OnDemandPrice)

	formatted, err := format.Source(src.Bytes())
	if err != nil {
		log.Fatalf("formatting generated source, %s", err)
	}

	if err := ioutil.WriteFile(flag.Arg(0), formatted, 0644); err != nil {
		log.Fatalf("writing output, %s", err)
	}
	runtime.GC()
	if err := pprof.WriteHeapProfile(f); err != nil {
		log.Fatal("could not write memory profile: ", err)
	}
}

func writePricing(src *bytes.Buffer, instanceNames []string, varName string, getPrice func(instanceType string) (float64, error)) {
	fmt.Fprintf(src, "var %s = map[string]float64{\n", varName)
	lineLen := 0
	sort.Strings(instanceNames)
	previousFamily := ""
	for _, instanceName := range instanceNames {
		segs := strings.Split(instanceName, ".")
		if len(segs) != 2 {
			log.Fatalf("parsing instance family %s, got %v", instanceName, segs)
		}
		price, err := getPrice(instanceName)
		if err != nil {
			continue
		}

		// separating by family should lead to smaller diffs instead of just breaking at line endings only
		family := segs[0]
		if family != previousFamily {
			previousFamily = family
			newline(src)
			fmt.Fprintf(src, "// %s family\n", family)
			lineLen = 0
		}

		n, err := fmt.Fprintf(src, `"%s":%f, `, instanceName, price)
		if err != nil {
			log.Fatalf("error writing, %s", err)
		}
		lineLen += n
		if lineLen > 80 {
			lineLen = 0
			fmt.Fprintln(src)
		}
	}
	fmt.Fprintln(src, "\n}")
	fmt.Fprintln(src)
}

// newline adds a newline to src, if it does not currently already end with a newline
func newline(src *bytes.Buffer) {
	contents := src.Bytes()
	// no content yet, so create the new line
	if len(contents) == 0 {
		fmt.Println(src)
		return
	}
	// already has a newline, so don't write a new one
	if contents[len(contents)-1] == '\n' {
		return
	}
	fmt.Fprintln(src)
}
