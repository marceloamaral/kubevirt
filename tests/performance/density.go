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
	"time"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"

	k8sv1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/watch"

	kvv1 "kubevirt.io/client-go/api/v1"
	cd "kubevirt.io/kubevirt/tests/containerdisk"

	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"

	"kubevirt.io/client-go/kubecli"
	"kubevirt.io/kubevirt/tests"
	"kubevirt.io/kubevirt/tests/util"

	e2emetrics "kubevirt.io/kubevirt/tests/performance/metrics"
)

type DensityTest struct {
	VMCount              int               `yaml:"vm_count"`                // total number of vms
	VMImage              cd.ContainerDisk  `yaml:"vm_image_name"`           // the image type will reflect in the storage size
	CPULimit             string            `yaml:"cpu_limit"`               // number of CPUs to allocate (100m = .1 cores)
	MemLimit             string            `yaml:"mem_limit"`               // amount of Memory to allocate
	VMStartupLimits      e2emetrics.Metric `yaml:"vm_startup_limits"`       // percentile limit of single vm startup latency
	VMBatchStartupLimit  time.Duration     `yaml:"vm_batch_startup_limit"`  // upbound of startup latency of a batch of vms
	VMDeletionLimit      time.Duration     `yaml:"vm_deletion_limit"`       // time limit of single vm deletion in seconds
	VMBatchDeletionLimit time.Duration     `yaml:"vm_batch_deletion_limit"` // time limit of a batch of vms deletion
	CloudInitUserData    string            `yaml:"cloud_init_user_data"`    // script to run into the VM during the startup
	GinkoLabel           string            `yaml:"ginko_label"`             // test label [small-scale] [medium-scale] [large-scale]
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
			VMCount:  100,
			VMImage:  "cirros",
			CPULimit: "100m",
			MemLimit: "90Mi",
			VMStartupLimits: e2emetrics.Metric{
				Perc: map[int]float64{
					50: 60.0,
					90: 70.0,
					99: 80.0,
				},
			},
			VMBatchStartupLimit:  300,
			VMDeletionLimit:      80,
			VMBatchDeletionLimit: 300,
			CloudInitUserData:    "#!/bin/bash\necho 'hello'\n",
			GinkoLabel:           "[small]",
		},
	}

	// Run all density tests
	Describe("Density test", func() {

		for _, testArg := range densityTests {
			Context(fmt.Sprintf("%s create a batch of %d VMIs", testArg.GinkoLabel, testArg.VMCount), func() {

				desc := fmt.Sprintf("latency should be within limit of %d seconds when create %d vms",
					int(testArg.VMBatchStartupLimit), testArg.VMCount)

				It(desc, func() {
					status := e2emetrics.NewBatchStatus()

					By("Starting vmi watcher")
					quit := make(chan bool)
					go watchVMI(virtClient, status, testArg, quit)

					By("Creating a batch of VMIs")
					fmt.Printf("\nCreating a batch of %d VMIs\n", testArg.VMCount)
					start := time.Now()
					createBatchVMWithRateControl(virtClient, testArg)
					By("Waiting a batch of VMIs")
					waitVMIBatchStatus(status, kvv1.Running, testArg.VMCount, testArg.VMBatchStartupLimit*time.Second)
					end := time.Now()
					batchStartupLag := end.Sub(start) / time.Second

					By("Deleting all VMIs")
					fmt.Println("Deleting all VMIs")
					start = time.Now()
					deleteVMIBatch(virtClient, status, testArg)
					waitVMIBatchStatus(status, kvv1.Succeeded, testArg.VMCount, testArg.VMBatchDeletionLimit*time.Second)
					end = time.Now()
					batchTerminationLag := end.Sub(start) / time.Second

					By("Stop vmi watcher")
					close(quit)

					By("Verifying latency")
					logAndVerifyVMCreationMetrics(batchStartupLag, batchTerminationLag, status.GetVMIMetrics(), testArg, true)
				})
			})
		}
	})

})

// createBatchVMWithRateControl creates a batch of vms concurrently, uses one goroutine for each creation.
// between creations there is an interval for throughput control
func createBatchVMWithRateControl(virtClient kubecli.KubevirtClient, testArg DensityTest) {
	defer GinkgoRecover()

	for i := 1; i <= testArg.VMCount; i++ {
		vmi := createVMISpecWithResources(virtClient, testArg)
		By(fmt.Sprintf("Creating VMI %s", vmi.ObjectMeta.Name))
		_, err := virtClient.VirtualMachineInstance(util.NamespaceTestDefault).Create(vmi)
		Expect(err).ToNot(HaveOccurred())

		// control the throughput with waiting interval
		time.Sleep(300 * time.Millisecond)
	}
}

func createVMISpecWithResources(virtClient kubecli.KubevirtClient, testArg DensityTest) *kvv1.VirtualMachineInstance {
	vmi := tests.NewRandomVMIWithEphemeralDiskAndUserdata(cd.ContainerDiskFor(testArg.VMImage), testArg.CloudInitUserData)
	vmi.Spec.Domain.Resources.Limits = k8sv1.ResourceList{
		k8sv1.ResourceMemory: resource.MustParse(testArg.MemLimit),
		k8sv1.ResourceCPU:    resource.MustParse(testArg.CPULimit),
	}
	vmi.Spec.Domain.Resources.Requests = k8sv1.ResourceList{
		k8sv1.ResourceMemory: resource.MustParse(testArg.MemLimit),
		k8sv1.ResourceCPU:    resource.MustParse(testArg.CPULimit),
	}
	return vmi
}

func watchVMI(virtClient kubecli.KubevirtClient, status *e2emetrics.BatchStatus, testArg DensityTest, quit chan bool) {
	defer GinkgoRecover()

	watcher, err := virtClient.VirtualMachineInstance(util.NamespaceTestDefault).Watch(metav1.ListOptions{})
	Expect(err).ToNot(HaveOccurred())

	// wait the VMI deletion status
	timeout := time.After(testArg.VMBatchDeletionLimit * time.Second)

	for {
		select {
		case <-quit:
			return
		case <-timeout:
			err = fmt.Errorf("failed to create %v VMIs within the expected interval %v",
				testArg.VMCount, testArg.VMBatchDeletionLimit*time.Second)
			Expect(err).ToNot(HaveOccurred())

		case event := <-watcher.ResultChan():
			handleVMIEvent(event, status, virtClient, testArg)
		}
	}
}

func handleVMIEvent(event watch.Event, status *e2emetrics.BatchStatus, virtClient kubecli.KubevirtClient, testArg DensityTest) error {
	defer GinkgoRecover()

	vmi, ok := event.Object.(*kvv1.VirtualMachineInstance)
	Expect(ok).To(BeTrue())

	switch event.Type {

	case watch.Modified:
		if vmi.Status.Phase == kvv1.Running {
			status.AddVMIPhaseMetrics(*vmi)
		}

	case watch.Deleted:
		go waitVMIDisapear(virtClient, vmi, status, testArg.VMDeletionLimit*time.Second)
	}

	return nil
}

// waitVMIDisapear measure the deletion time without watching events due to scalability reasons
func waitVMIDisapear(virtClient kubecli.KubevirtClient, vmi *kvv1.VirtualMachineInstance, status *e2emetrics.BatchStatus, timeout time.Duration) {
	defer GinkgoRecover()

	// for high accuracy, we watch the phases with a very small pooling interval
	pollingInterval := 300 * time.Millisecond

	By(fmt.Sprintf("Waiting VMI %s disapear", vmi.ObjectMeta.Name))
	Eventually(func() error {
		_, err := virtClient.VirtualMachineInstance(vmi.Namespace).Get(vmi.ObjectMeta.Name, &metav1.GetOptions{})
		return err
	}, timeout, pollingInterval).Should(
		SatisfyAll(HaveOccurred(), WithTransform(errors.IsNotFound, BeTrue())),
		"The VMI should be gone within the given timeout",
	)

	status.AddVMIDeletionMetrics(*vmi)
}

func deleteVMIBatch(virtClient kubecli.KubevirtClient, status *e2emetrics.BatchStatus, testArg DensityTest) {
	status.RLock()
	for _, data := range status.GetVMIPhaseMap(kvv1.Running) {
		// the GracePeriod is set to force the server to set the DeletionTimestamp
		gracePeriodSeconds := int64(0)
		err := virtClient.VirtualMachineInstance(data.Namespace).Delete(data.Name, &metav1.DeleteOptions{GracePeriodSeconds: &gracePeriodSeconds})
		Expect(err).To(BeNil())
		time.Sleep(200 * time.Millisecond)
	}
	status.RUnlock()
}

func waitVMIBatchStatus(status *e2emetrics.BatchStatus, phase kvv1.VirtualMachineInstancePhase, vmiCount int, timeout time.Duration) error {
	for timeout := time.After(timeout); ; {
		select {
		case <-timeout:
			return fmt.Errorf("failed to create %v VMIs within the expected interval %v", vmiCount, timeout)

		default:
			if status.PhaseSize(phase) >= vmiCount {
				return nil
			}
			time.Sleep(5 * time.Second)
			fmt.Println(status.PhaseSize(phase), phase, " VMs")
		}
	}
}

// logAndVerifyVMCreationMetrics verifies that whether vm creation latency satisfies the limit.
func logAndVerifyVMCreationMetrics(batchStartupLag time.Duration, batchTerminationLag time.Duration, e2eData map[kvv1.VirtualMachineInstancePhase]e2emetrics.Slice,
	testArg DensityTest, isVerify bool) {

	if isVerify {
		vmLatencies := make(map[kvv1.VirtualMachineInstancePhase]e2emetrics.Metric)
		vmLatencies[kvv1.Running] = e2emetrics.ExtractMetrics(e2eData[kvv1.Running].Sort())

		// check whether e2e vm startup time is acceptable.
		By("Verify latency within threshold")
		Expect(e2emetrics.VerifyMetricWithinThreshold(testArg.VMStartupLimits, vmLatencies[kvv1.Running], "vm startup")).ToNot(HaveOccurred())

		// check bactch vm creation latency
		if testArg.VMBatchStartupLimit > 0 {
			By("VM Batch Startup Limit")
			Expect(batchStartupLag <= testArg.VMBatchStartupLimit*time.Second).To(Equal(true))
		}
		fmt.Printf("VM Batch Startup Time %v", batchStartupLag)

		// check bactch vm creation latency
		if testArg.VMBatchDeletionLimit > 0 {
			By("VM Batch Deletion Limit")
			Expect(batchTerminationLag <= testArg.VMBatchDeletionLimit*time.Second).To(Equal(true))
		}
		fmt.Printf("VM Batch Deletion Time %v", batchTerminationLag)
	}

	// Print aggregated data for Running phase
	for phase := range e2eData {
		interval := fmt.Sprintf("Creation to %s", string(phase))
		if phase == kvv1.Succeeded {
			interval = "Running to Deleted"
		}
		e2emetrics.PrintMetrics(e2eData[phase].Sort(),
			fmt.Sprintf("worst client e2e latencies from %s", interval))
	}

}
