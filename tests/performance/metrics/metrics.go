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

package metrics

import (
	"fmt"
	"math"
	"sort"
	"sync"
	"time"

	kvv1 "kubevirt.io/client-go/api/v1"
)

var (
	Percentiles = []int{50, 90, 99}
)

type MetricType int

type Unit string

// Metric is a struct for metrics for performance tests, it can be latencies or resource usage.
type Metric struct {
	Perc map[int]float64 `yaml:"perc50"`
	Unit Unit            `yaml:"unit,omitempty"`
}

// VMMetricData encapsulates vm startup latency information.
type VMMetricData struct {
	// Name of the vm
	Name string
	// Node this vm was running on
	Node string
	// Metric value
	Value float64
	// Metric unit
	Unit Unit
	// Type can be latency (e.g., for each VM creation phases), or resource (e.g., of resource usage)
	Type kvv1.VirtualMachineInstancePhase
}

// Slice is an array of VMMetricData which encapsulates vm startup latency information.
type Slice []VMMetricData

func (a Slice) Len() int           { return len(a) }
func (a Slice) Swap(i, j int)      { a[i], a[j] = a[j], a[i] }
func (a Slice) Less(i, j int) bool { return a[i].Value < a[j].Value }

func (a Slice) Sort() Slice { sort.Sort(a); return a }

// ExtractMetrics returns latency metrics for each percentile(50th, 90th and 99th).
func ExtractMetrics(data []VMMetricData) Metric {
	metrics := Metric{
		Perc: make(map[int]float64),
	}

	length := len(data)
	if length < 1 {
		return metrics
	}

	for _, percentile := range Percentiles {
		metrics.Perc[percentile] = data[int(math.Ceil(float64(length*percentile)/100))-1].Value
	}
	metrics.Unit = data[0].Unit
	return metrics
}

// PrintMetrics outputs metrics to log with readable format.
func PrintMetrics(metric []VMMetricData, header string) {
	metrics := ExtractMetrics(metric)
	perc10 := metric[(len(metric)*9)/10:]
	if len(perc10) > 0 {
		fmt.Printf("\n10%% %s: %s %f in %s \n",
			header, perc10[0].Name, perc10[0].Value, metrics.Unit)
	}

	for _, percentile := range Percentiles {
		fmt.Printf("perc%d: %f, ",
			percentile, metrics.Perc[50])
	}

	fmt.Printf("in %s \n", metrics.Unit)
}

// VerifyMetricWithinThreshold verifies whether 50, 90 and 99th percentiles of a latency metric are within the expected threshold.
func VerifyMetricWithinThreshold(threshold, actual Metric, metricName string) error {
	for _, percentile := range Percentiles {
		if actual.Perc[percentile] > threshold.Perc[percentile] {
			return fmt.Errorf("too high %v latency %dth percentile: %f, expected max %f",
				metricName, percentile, actual.Perc[percentile], threshold.Perc[percentile])
		}
	}

	return nil
}

// BatchStatus struct contains a map with the VMI metrics and another method for quick lookup
type BatchStatus struct {
	sync.RWMutex
	metrics      map[kvv1.VirtualMachineInstancePhase]Slice
	vmisByPhases map[kvv1.VirtualMachineInstancePhase]map[string]kvv1.VirtualMachineInstance
}

// NewBatchStatus creates a new BatchStatus
func NewBatchStatus() *BatchStatus {
	s := &BatchStatus{
		metrics:      make(map[kvv1.VirtualMachineInstancePhase]Slice),
		vmisByPhases: make(map[kvv1.VirtualMachineInstancePhase]map[string]kvv1.VirtualMachineInstance),
	}
	s.vmisByPhases[kvv1.Scheduling] = make(map[string]kvv1.VirtualMachineInstance)
	s.vmisByPhases[kvv1.Scheduled] = make(map[string]kvv1.VirtualMachineInstance)
	s.vmisByPhases[kvv1.Running] = make(map[string]kvv1.VirtualMachineInstance)
	s.vmisByPhases[kvv1.Succeeded] = make(map[string]kvv1.VirtualMachineInstance)
	return s
}

// GetVMIPhaseMap returns the list of VMI for a given phase
func (s *BatchStatus) GetVMIPhaseMap(phase kvv1.VirtualMachineInstancePhase) map[string]kvv1.VirtualMachineInstance {
	return s.vmisByPhases[phase]
}

// GetVMIPhaseMap returns the list of all VMI metrics
func (s *BatchStatus) GetVMIMetrics() map[kvv1.VirtualMachineInstancePhase]Slice {
	return s.metrics
}

// AddVMIPhaseMetrics add metrics for all VMI phases execpt when deleted, which is computed differently
func (s *BatchStatus) AddVMIPhaseMetrics(vmi kvv1.VirtualMachineInstance) {
	s.Lock()
	defer s.Unlock()

	if _, ok := s.vmisByPhases[kvv1.Running][vmi.ObjectMeta.Name]; !ok {
		creationTs := vmi.ObjectMeta.CreationTimestamp.Time
		for _, phaseTrans := range vmi.Status.PhaseTransitionTimestamps {
			if phaseTrans.Phase == kvv1.Scheduling || phaseTrans.Phase == kvv1.Scheduled || phaseTrans.Phase == kvv1.Running {
				timestamp := phaseTrans.PhaseTransitionTimestamp.Time
				latencyData := VMMetricData{
					Name:  vmi.ObjectMeta.Name,
					Value: float64(timestamp.Sub(creationTs).Nanoseconds()) / float64(time.Second),
					Unit:  "second",
				}
				s.metrics[phaseTrans.Phase] = append(s.metrics[phaseTrans.Phase], latencyData)
				s.vmisByPhases[phaseTrans.Phase][vmi.ObjectMeta.Name] = vmi
			}
		}
	}
}

// AddVMIDeletionMetrics uses the DeletionTimestamp to compute the deletion time
func (s *BatchStatus) AddVMIDeletionMetrics(vmi kvv1.VirtualMachineInstance) {
	s.Lock()
	defer s.Unlock()

	if _, ok := s.vmisByPhases[kvv1.Succeeded][vmi.ObjectMeta.Name]; !ok {
		// DeletionTimestamp requires that the GracePeriodSeconds was set during the deletion
		deletionTimestamp := vmi.ObjectMeta.DeletionTimestamp.Time
		latencyData := VMMetricData{
			Name:  vmi.ObjectMeta.Name,
			Value: float64(time.Since(deletionTimestamp).Nanoseconds()) / float64(time.Second),
			Unit:  "second",
		}
		s.metrics[kvv1.Succeeded] = append(s.metrics[kvv1.Succeeded], latencyData)
		s.vmisByPhases[kvv1.Succeeded][vmi.ObjectMeta.Name] = vmi
	}
}

// PhaseSize returns the number of VMIs in the given phase
func (s *BatchStatus) PhaseSize(phase kvv1.VirtualMachineInstancePhase) int {
	s.RLock()
	defer s.RUnlock()
	return len(s.metrics[phase])
}
