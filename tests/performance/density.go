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
	"fmt"
	"strings"
	"time"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"

	k8sv1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kvv1 "kubevirt.io/client-go/api/v1"
	cd "kubevirt.io/kubevirt/tests/containerdisk"

	"k8s.io/apimachinery/pkg/api/errors"

	"kubevirt.io/client-go/kubecli"
	"kubevirt.io/kubevirt/tests"
	"kubevirt.io/kubevirt/tests/util"

	e2emetrics "kubevirt.io/kubevirt/tests/performance/metrics"
	"kubevirt.io/kubevirt/tests/performance/perftype"
)

type DensityTest struct {
	NumVMs               int               `yaml:"num_vms"`                 // number of vms
	NumBGVMs             int               `yaml:"num_bg_vms"`              // number of background vms
	ArrivalRate          float64           `yaml:"arrival_rate"`            // number fo vms per second
	ArrivalUnit          time.Duration     `yaml:"arrival_unit"`            // to convert from second to another unit
	ArrivalRateFuncName  string            `yaml:"arrival_rate_func_name"`  // e.g. using a constant interval or a poisson process
	VMImage              cd.ContainerDisk  `yaml:"vm_image_name"`           // the image type will reflect in the storage size
	CPULimit             string            `yaml:"cpu_limit"`               // number of CPUs to allocate (100m = .1 cores)
	MEMLimit             string            `yaml:"mem_limit"`               // amount of Memory to allocate
	VMStartupLimits      e2emetrics.Metric `yaml:"vm_startup_limits"`       // percentile limit of single vm startup latency
	VMBatchStartupLimit  time.Duration     `yaml:"vm_batch_startup_limit"`  // upbound of startup latency of a batch of vms
	VMDeletionLimit      time.Duration     `yaml:"vm_deletion_limit"`       // time limit of single vm deletion in seconds
	VMBatchDeletionLimit time.Duration     `yaml:"vm_batch_deletion_limit"` // time limit of a batch of vms deletion
	CloudInitUserData    string            `yaml:"cloud_init_user_data"`    // script to run into the VM during the startup
	GinkoLabel           string            `yaml:"ginko_label"`             // test label [small-scale] [medium-scale] [large-scale]
}

// ArrivalRateFunc generate arrival time. The default is a stepped load with an interval of 300 miliseconds
func (t *DensityTest) ArrivalRateFunc(arrivalRate float64) time.Duration {
	if strings.Contains(string(t.ArrivalRateFuncName), "poisson") {
		return poissonProcess(arrivalRate)
	}
	// ArrivalRate is the number fo vms per second. Then, we calculate a stepped interval between VMs
	if t.ArrivalRate > 0 {
		return time.Duration(1/t.ArrivalRate) * time.Millisecond
	}
	return time.Duration(300) * time.Millisecond
}

var _ = SIGDescribe("Control Plane Performance Density Testing", func() {
	var (
		err        error
		virtClient kubecli.KubevirtClient
	)

	BeforeEach(func() {
		virtClient, err = kubecli.GetKubevirtClient()
		util.PanicOnError(err)
		tests.BeforeTestCleanup()
	})

	densityTests := []DensityTest{
		{
			NumVMs:              5,
			NumBGVMs:            20,
			ArrivalRate:         0.002, // Vms per second
			ArrivalUnit:         time.Millisecond,
			ArrivalRateFuncName: "poisson",
			VMImage:             "cirros",
			CPULimit:            "100m",
			MEMLimit:            "90Mi",
			VMStartupLimits: e2emetrics.Metric{
				Perc50: 350,
				Perc90: 400,
				Perc99: 450,
			},
			VMBatchStartupLimit:  400,
			VMDeletionLimit:      250,
			VMBatchDeletionLimit: 400,
			CloudInitUserData:    "#!/bin/bash\necho 'hello'\n",
			GinkoLabel:           "[small]",
		},
	}

	// Run all density tests
	Describe("Density test", func() {

		for _, testArg := range densityTests {
			Context(fmt.Sprintf("%s create a batch of %d VMIs", testArg.GinkoLabel, testArg.NumVMs), func() {

				desc := fmt.Sprintf("latency should be within limit when create %d vms with rate of %v/second", testArg.NumVMs, testArg.ArrivalRate)
				It(desc, func() {
					batchStartupLag, batchTerminationLag, e2eData := runDensityBatchTest(virtClient, testArg)

					By("Verifying latency")
					logAndVerifyVMCreationMetrics(batchStartupLag, batchTerminationLag, e2eData, testArg, true)

				})
			})
		}
	})

})

// runDensityBatchTest runs the density batch vm creation test
func runDensityBatchTest(virtClient kubecli.KubevirtClient, testArg DensityTest) (time.Duration, time.Duration, map[kvv1.VirtualMachineInstancePhase]e2emetrics.Slice) {
	var (
		queue   = make(chan map[kvv1.VirtualMachineInstancePhase]e2emetrics.VMMetricData)
		e2eData = make(map[kvv1.VirtualMachineInstancePhase]e2emetrics.Slice)
	)

	By("Creating a batch of vms")
	firstCreate := time.Now()
	vmList := createBatchVMWithRateControl(virtClient, testArg, queue)
	By("Waiting for all VMIs become ready...")
	waitResults(queue, &e2eData, testArg, kvv1.Running, testArg.VMBatchStartupLimit)

	lastRunning := time.Now()
	batchStartupLag := lastRunning.Sub(firstCreate) / time.Second
	fmt.Printf("total batch VMI startup latency %d", batchStartupLag)

	By("Deleting all VirtualMachineInstance")
	firstDelete := time.Now()
	queue = make(chan map[kvv1.VirtualMachineInstancePhase]e2emetrics.VMMetricData)
	for _, vmi := range vmList {
		go watchVMIDeletion(virtClient, queue, vmi, testArg.VMDeletionLimit*time.Second)
	}
	By("Wait all VirtualMachineInstance Disappear")
	waitResults(queue, &e2eData, testArg, kvv1.Succeeded, testArg.VMDeletionLimit)

	lastRunning = time.Now()
	batchTerminationLag := lastRunning.Sub(firstDelete) / time.Second
	fmt.Printf("total batch VMI deletetion latency %d", batchStartupLag)

	return batchStartupLag, batchTerminationLag, e2eData
}

func waitResults(queue chan map[kvv1.VirtualMachineInstancePhase]e2emetrics.VMMetricData, e2eData *map[kvv1.VirtualMachineInstancePhase]e2emetrics.Slice,
	testArg DensityTest, stopType kvv1.VirtualMachineInstancePhase, timout time.Duration) {
	timeoutq := time.After(timout * time.Second)
	var err error
startupChannel:
	for {
		select {
		case <-timeoutq: // timed out
			err = fmt.Errorf("failed for %s of %v VMIs from %v within the expected interval %v",
				stopType,
				testArg.NumVMs-len((*e2eData)[stopType]),
				testArg.NumVMs, testArg.VMBatchStartupLimit*time.Second)
			break startupChannel

		case e2eD := <-queue:
			for metric, val := range e2eD {
				(*e2eData)[metric] = append((*e2eData)[metric], val)
			}

			if len((*e2eData)[stopType]) == testArg.NumVMs {
				err = nil
				break startupChannel
			}
		}
	}
	Expect(err).ToNot(HaveOccurred())
}

// createBatchVMWithRateControl creates a batch of vms concurrently, uses one goroutine for each creation.
// between creations there is an interval for throughput control
func createBatchVMWithRateControl(virtClient kubecli.KubevirtClient, testArg DensityTest,
	queue chan map[kvv1.VirtualMachineInstancePhase]e2emetrics.VMMetricData) map[string]*kvv1.VirtualMachineInstance {
	defer GinkgoRecover()
	vmList := make(map[string]*kvv1.VirtualMachineInstance)

	for i := 1; i <= testArg.NumVMs; i++ {
		vmi := createVMISpecWithResources(virtClient, testArg)
		vmList[vmi.ObjectMeta.Name] = vmi

		// watch VMIs in parallel until they are ready
		// golang allows hundreds of thousands goroutines in the same address space with very low overhead
		go watchVMIPhases(virtClient, testArg, queue, vmi)

		// control the throughput with waiting interval
		interval := testArg.ArrivalRateFunc(testArg.ArrivalRate)
		time.Sleep(interval * testArg.ArrivalUnit)
	}

	return vmList
}

func createVMISpecWithResources(virtClient kubecli.KubevirtClient, testArg DensityTest) *kvv1.VirtualMachineInstance {
	vmi := tests.NewRandomVMIWithEphemeralDiskAndUserdata(cd.ContainerDiskFor(testArg.VMImage), testArg.CloudInitUserData)
	vmi.Spec.Domain.Resources.Limits = k8sv1.ResourceList{
		k8sv1.ResourceMemory: resource.MustParse(testArg.MEMLimit),
		k8sv1.ResourceCPU:    resource.MustParse(testArg.CPULimit),
	}
	vmi.Spec.Domain.Resources.Requests = k8sv1.ResourceList{
		k8sv1.ResourceMemory: resource.MustParse(testArg.MEMLimit),
		k8sv1.ResourceCPU:    resource.MustParse(testArg.CPULimit),
	}
	return vmi
}

func watchVMIPhases(virtClient kubecli.KubevirtClient, testArg DensityTest, queue chan map[kvv1.VirtualMachineInstancePhase]e2emetrics.VMMetricData,
	vmi *kvv1.VirtualMachineInstance) {
	defer GinkgoRecover()

	e2eData := make(map[kvv1.VirtualMachineInstancePhase]e2emetrics.VMMetricData)

	By(fmt.Sprintf("Creating VMI %s and wait it to start running", vmi.ObjectMeta.Name))
	_, err := virtClient.VirtualMachineInstance(util.NamespaceTestDefault).Create(vmi)
	Expect(err).ToNot(HaveOccurred())

	tests.WaitForSuccessfulVMIStartWithTimeoutIgnoreWarnings(vmi, int(testArg.VMStartupLimits.Perc99))
	By(fmt.Sprintf("VMI %s is running %v", vmi.ObjectMeta.Name, vmi.Status.PhaseTransitionTimestamps))

	var createts metav1.Time
	for _, phaseTrans := range vmi.Status.PhaseTransitionTimestamps {
		if phaseTrans.Phase == kvv1.VmPhaseUnset {
			// creation timestamp is when a VirtualMachineInstance Object is first initialized
			createts = phaseTrans.PhaseTransitionTimestamp
			continue
		}
		timestamp := phaseTrans.PhaseTransitionTimestamp
		latencyData := e2emetrics.VMMetricData{
			Name:  vmi.ObjectMeta.Name,
			Value: float64(timestamp.Time.Sub(createts.Time)) / float64(time.Second),
			Unit:  e2emetrics.Seconds,
		}
		e2eData[phaseTrans.Phase] = latencyData
		By(fmt.Sprintf("VMI %s phase %s, %f seconds ", vmi.ObjectMeta.Name, phaseTrans.Phase, latencyData.Value))
	}

	queue <- e2eData
}

// watchVMIDeletion measure the deletion time without watching events due to scalability reasons
func watchVMIDeletion(virtClient kubecli.KubevirtClient, queue chan map[kvv1.VirtualMachineInstancePhase]e2emetrics.VMMetricData,
	vmi *kvv1.VirtualMachineInstance, timeout time.Duration) {
	defer GinkgoRecover()

	// for high accuracy, we watch the phases with a small polling interval
	pollingInterval := 300 * time.Millisecond
	e2eData := make(map[kvv1.VirtualMachineInstancePhase]e2emetrics.VMMetricData)

	By(fmt.Sprintf("Deleting VMI %s", vmi.ObjectMeta.Name))
	terminatingTime := time.Now()
	err := virtClient.VirtualMachineInstance(vmi.Namespace).Delete(vmi.Name, &metav1.DeleteOptions{})
	Expect(err).To(BeNil())

	Eventually(func() error {
		_, err := virtClient.VirtualMachineInstance(vmi.Namespace).Get(vmi.Name, &metav1.GetOptions{})
		return err
	}, timeout, pollingInterval).Should(
		SatisfyAll(HaveOccurred(), WithTransform(errors.IsNotFound, BeTrue())),
		"The VMI should be gone within the given timeout",
	)

	deleteTime := time.Now()
	latencyData := e2emetrics.VMMetricData{
		Name:  vmi.ObjectMeta.Name,
		Value: float64(deleteTime.Sub(terminatingTime)) / float64(time.Second),
		Unit:  e2emetrics.Seconds,
	}
	e2eData[kvv1.Succeeded] = latencyData
	By(fmt.Sprintf("VMI %s terminated in %.4f s", vmi.ObjectMeta.Name, float64(time.Duration(latencyData.Value)/time.Second)))

	queue <- e2eData
}

// logAndVerifyVMCreationMetrics verifies that whether vm creation latency satisfies the limit.
func logAndVerifyVMCreationMetrics(batchStartupLag time.Duration, batchTerminationLag time.Duration, e2eData map[kvv1.VirtualMachineInstancePhase]e2emetrics.Slice,
	testArg DensityTest, isVerify bool) {
	vmLatencies := make(map[kvv1.VirtualMachineInstancePhase]e2emetrics.Metric)

	for phase := range e2eData {
		vmLatencies[phase] = e2emetrics.ExtractMetrics(e2eData[phase].Sort())
	}

	if isVerify {
		// check whether e2e vm startup time is acceptable.
		By("Verify latency within threshold")
		Expect(VerifyMetricWithinThreshold(testArg.VMStartupLimits, vmLatencies[kvv1.Running], "vm startup")).ToNot(HaveOccurred())

		// check bactch vm creation latency
		if testArg.VMBatchStartupLimit > 0 {
			By("VM Batch Startup Limit")
			Expect(batchStartupLag <= testArg.VMBatchStartupLimit*time.Second).To(Equal(true))
		}

		// check bactch vm creation latency
		if testArg.VMBatchDeletionLimit > 0 {
			By("VM Batch Termination Limit")
			Expect(batchTerminationLag <= testArg.VMBatchDeletionLimit*time.Second).To(Equal(true))
		}
	}

	// Print aggregated data for Running phase
	PrintMetrics(e2eData[kvv1.Running].Sort(), "worst client e2e total latencies")

	// log latency perf data
	perfData := perftype.GetPerfData(vmLatencies, "density-test")
	LogPerfData(perfData, "vmi")
}
