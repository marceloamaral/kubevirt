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

package perftype

import (
	kvv1 "kubevirt.io/client-go/api/v1"

	e2emetrics "kubevirt.io/kubevirt/tests/performance/metrics"
)

// DataItem is the data point.
type DataItem struct {
	// Data is a map from bucket to real data point (e.g. "Perc90" -> 23.5). Notice
	// that all data items with the same label combination should have the same buckets.
	Data map[string]float64 `json:"data"`
	// Unit is the data unit. Notice that all data items with the same label combination
	// should have the same unit.
	Unit string `json:"unit"`
	// Labels is the labels of the data item.
	Labels map[string]string `json:"labels,omitempty"`
}

// PerfData contains all data items generated in current test.
type PerfData struct {
	// Version is the version of the metrics. The metrics consumer could use the version
	// to detect metrics version change and decide what version to support.
	Version   string     `json:"version"`
	DataItems []DataItem `json:"dataItems"`
	// Labels is the labels of the dataset.
	// The Labels can be used to store information from the node that VM was running.
	Labels map[string]string `json:"labels,omitempty"`
}

// GetPerfData returns perf data of VMI startup latency.
func GetPerfData(vmMetrics map[kvv1.VirtualMachineInstancePhase]e2emetrics.Metric, test string) *PerfData {
	pd := PerfData{
		Version:   "",
		DataItems: make([]DataItem, 0),
		Labels: map[string]string{
			"Test": test,
		},
	}

	for metric, value := range vmMetrics {
		dt := DataItem{
			Data: map[string]float64{
				"Perc50": value.Perc50,
				"Perc90": value.Perc90,
				"Perc99": value.Perc99,
			},
			Unit: string(value.Unit),
			Labels: map[string]string{
				"Metric": string(metric),
			},
		}

		pd.DataItems = append(pd.DataItems, dt)
	}

	return &pd
}

// PerfResultTag is the prefix of generated perfdata. Analyzing tools can find the perf result with this tag.
const PerfResultTag = "[Performance:Result]"

// PerfResultEnd is the end of generated perfdata. Analyzing tools can find the end of the perf result with this tag.
const PerfResultEnd = "[Performance:ResultFinished]"
