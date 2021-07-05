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
	"net"
	"time"

	"math"
	"math/rand"

	e2emetrics "kubevirt.io/kubevirt/tests/performance/metrics"
)

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

// PrintMetrics outputs metrics to log with readable format.
func PrintMetrics(metric []e2emetrics.VMMetricData, header string) {
	metrics := e2emetrics.ExtractMetrics(metric)
	perc10 := metric[(len(metric)*9)/10:]
	if len(perc10) > 0 {
		fmt.Printf("\n10%% %s: %s %f in %s \n",
			header, perc10[0].Name, perc10[0].Value, metrics.Unit)
	}
	fmt.Printf("perc50: %f, perc90: %f, perc99: %f in %s \n",
		metrics.Perc50, metrics.Perc90, metrics.Perc99, metrics.Unit)
}

// VerifyMetricWithinThreshold verifies whether 50, 90 and 99th percentiles of a latency metric are within the expected threshold.
func VerifyMetricWithinThreshold(threshold, actual e2emetrics.Metric, metricName string) error {
	if actual.Perc50 > threshold.Perc50 {
		return fmt.Errorf("too high %v latency 50th percentile: %f, expected max %f", metricName, actual.Perc50, threshold.Perc50)
	}
	if actual.Perc90 > threshold.Perc90 {
		return fmt.Errorf("too high %v latency 90th percentile: %f, expected max %f", metricName, actual.Perc90, threshold.Perc90)
	}
	if actual.Perc99 > threshold.Perc99 {
		return fmt.Errorf("too high %v latency 99th percentile: %f, expected max %f", metricName, actual.Perc99, threshold.Perc99)
	}
	return nil
}

// GatewayIpFromCIDR returns the first address of a network.
func GatewayIPFromCIDR(cidr string) string {
	ip, ipnet, _ := net.ParseCIDR(cidr)
	ip = ip.Mask(ipnet.Mask)
	oct := len(ip) - 1
	ip[oct]++
	return ip.String()
}
