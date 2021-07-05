/*
 * This file is part of the KubeVirt project
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 *
 * Copyright 2021 Red Hat, Inc.
 *
 */

package performance

import (
	"flag"
	"fmt"
	"io/ioutil"
	"math"
	"math/rand"
	"os"
	"strings"
	"time"

	"kubevirt.io/kubevirt/tests/console"
	cd "kubevirt.io/kubevirt/tests/containerdisk"
	"kubevirt.io/kubevirt/tests/libnet"

	"gopkg.in/yaml.v2"

	e2emetrics "kubevirt.io/kubevirt/tests/performance/metrics"
)

var testDir = os.Getenv("TESTS_OUT_DIR")
var PerfConfigFile = testDir + "/performance/perf_tests_conf.yaml"
var CreatePerfReport = false
var artifactsDir = os.Getenv("ARTIFACTS")
var PerfReportDir = artifactsDir + "/report"
var RunPerfTests = false

func init() {
	flag.StringVar(&PerfConfigFile, "performance-config-path", PerfConfigFile, "absolute path to directory that contains the performance tests configuration file.")
	flag.BoolVar(&CreatePerfReport, "performance-report", true, "create performance report into performance-report-dir.")
	flag.StringVar(&PerfReportDir, "performance-report-dir", PerfReportDir, "absolute path to directory to store the performance test results.")

	flag.BoolVar(&RunPerfTests, "performance-test", false, "run performance tests. If false, all performance tests will be skiped.")
	ptest := os.Getenv("KUBEVIRT_E2E_PERF_TEST")
	if ptest == "true" || ptest == "True" {
		RunPerfTests = true
	}
}

type Perftest struct {
	DensityTests []DensityTest `yaml:"density_tests"`
}

type DensityTest struct {
	NumVMs               int               `yaml:"num_vms"`                            // number of vms
	NumBGVMs             int               `yaml:"num_bg_vms"`                         // number of background vms
	ArrivalRate          float64           `yaml:"arrival_rate"`                       // number fo vms per second
	ArrivalUnit          time.Duration     `yaml:"arrival_unit"`                       // to convert from second to another unit
	ArrivalRateFuncName  string            `yaml:"arrival_rate_func_name"`             // e.g. using a constant interval or a poisson process
	VMImage              cd.ContainerDisk  `yaml:"vm_image_name"`                      // the image type will reflect in the storage size
	CPULimit             string            `yaml:"cpu_limit"`                          // number of CPUs to allocate (100m = .1 cores)
	MEMLimit             string            `yaml:"mem_limit"`                          // amount of Memory to allocate
	VMStartupLimits      e2emetrics.Metric `yaml:"vm_startup_limits"`                  // percentile limit of single vm startup latency
	VMBatchStartupLimit  time.Duration     `yaml:"vm_batch_startup_limit"`             // upbound of startup latency of a batch of vms
	VMDeletionLimit      time.Duration     `yaml:"vm_deletion_limit"`                  // time limit of single vm deletion in seconds
	VMBatchDeletionLimit time.Duration     `yaml:"vm_batch_deletion_limit"`            // time limit of a batch of vms deletion
	WaitVMBoot           bool              `default:"false" yaml:"wait_vm_boot"`       // wait VM be fully ready until it can reply a ping command
	WaitVMReplyPing      bool              `default:"false" yaml:"wait_vm_reply_ping"` // wait VM be fully ready until it can reply a ping command
	CloudInitUserData    string            `yaml:"cloud_init_user_data"`               // script to run into the VM during the startup
	GinkoLabel           string            `yaml:"ginko_label"`                        // test label [small-scale] [medium-scale] [large-scale]
}

func (t *DensityTest) LoginMethod() console.LoginToFactory {
	if strings.Contains(string(t.VMImage), "cirros") {
		return libnet.WithIPv6(console.LoginToCirros)

	} else if strings.Contains(string(t.VMImage), "alpine") {
		return libnet.WithIPv6(console.LoginToAlpine)

	} else if strings.Contains(string(t.VMImage), "fedora-sriov-lane") {
		return libnet.WithIPv6(console.LoginToFedora)

	} else if strings.Contains(string(t.VMImage), "fedora-with-test-tooling") {
		return libnet.WithIPv6(console.LoginToFedora)

	} else if strings.Contains(string(t.VMImage), "fedora") {
		return libnet.WithIPv6(console.LoginToFedora)
	}
	return nil
}

// ArrivalRateFunc generate arrival time. The default is a stepped load with an interval of 300 miliseconds
func (t *DensityTest) ArrivalRateFunc(arrivalRate float64) time.Duration {
	if strings.Contains(string(t.ArrivalRateFuncName), "poisson") {
		return poissonProcess(arrivalRate)
	}
	if t.ArrivalRate > 0 {
		// ArrivalRate is the number fo vms per second. Then, we calculate a stepped interval between VMs
		return time.Duration(1/t.ArrivalRate) * time.Millisecond
	}
	return time.Duration(300) * time.Millisecond
}

var r *rand.Rand

// poissonProcess returns arrival interval time for a given lambda
func poissonProcess(lambda float64) time.Duration {
	if r == nil {
		// init random number generator
		r = rand.New(rand.NewSource(99))
	}

	randNumber := r.Float64()
	inter_arrival_time := -math.Log(1.0-randNumber) / float64(lambda)

	return time.Duration(inter_arrival_time)
}

// LoadTestConfig loads all performance tests from the configuration file perf_test_conf.yaml.
// The intention to do not have the parameter had-coded is because the limits might change accross different environments.
func LoadTestConfig() (Perftest, error) {
	t := Perftest{}

	data, err := ioutil.ReadFile(PerfConfigFile)
	if err != nil {
		fmt.Printf("Failed to read conf file: %v \n", err)
		return t, err
	}

	err = yaml.Unmarshal(data, &t)
	if err != nil {
		fmt.Printf("Failed to Unmarshal YAML file: %v \n", err)
		return t, err
	}

	return t, nil
}
