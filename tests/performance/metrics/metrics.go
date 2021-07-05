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
	"math"
	"sort"

	kvv1 "kubevirt.io/client-go/api/v1"
)

type MetricType int

type Unit string

// These are the valid statuses of pods.
const (
	Seconds    = "seconds"
	MegaBytes  = "MB"
	Percentage = "percentage"
)

// Metric is a struct for metrics for performance tests, it can be latencies or resource usage.
type Metric struct {
	Perc50 float64 `yaml:"perc50"`
	Perc90 float64 `yaml:"perc90"`
	Perc99 float64 `yaml:"perc99"`
	Unit   Unit    `yaml:"unit,omitempty"`
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
	metrics := Metric{}

	length := len(data)
	if length < 1 {
		return metrics
	}

	metrics.Perc50 = data[int(math.Ceil(float64(length*50)/100))-1].Value
	metrics.Perc90 = data[int(math.Ceil(float64(length*90)/100))-1].Value
	metrics.Perc99 = data[int(math.Ceil(float64(length*99)/100))-1].Value
	metrics.Unit = data[0].Unit
	return metrics
}
