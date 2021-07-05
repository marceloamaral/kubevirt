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

package resourcecollector

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	. "github.com/onsi/gomega"

	"kubevirt.io/client-go/kubecli"

	k8sv1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"kubevirt.io/kubevirt/tests"

	apiv1 "kubevirt.io/client-go/api/v1"

	prom_api "github.com/prometheus/client_golang/api"
	prom_v1 "github.com/prometheus/client_golang/api/prometheus/v1"
	"github.com/prometheus/common/model"
	prom_model "github.com/prometheus/common/model"
)

const (
	namespace               = "monitoring"
	prometheusSecret        = "prometheus-k8s-token"
	prometheusService       = "prometheus-k8s"
	localPrometheusEndpoint = "127.0.0.1"
	localPrometheusPort     = 9090

	// vmiMEMQuery query returns the memory usage reported by the VMI
	vmiMEMQuery = `kubevirt_vmi_memory_resident_bytes{namespace="%s",name="%s"}`
	// vmiCPUQuery query returns the CPU usage reported by the VMI
	vmiCPUQuery = `kubevirt_vmi_vcpu_seconds{namespace="%s",name="%s"}`
	// podMEMQuery query returns the memory usage of all containers in the pod related to the VMI.
	podMEMQuery = `sum(container_memory_working_set_bytes{%s})by(pod)`
	// podCPUQuery query returns the CPU usage of all containers in the pod related to the VMI
	podCPUQuery = `sum(rate(container_cpu_usage_seconds_total{%s}[5m]))by(pod)`
	// apiRequestDuration query returns the 99-percentile of the kubevirt related rest api request latency
	apiRequestDuration = `histogram_quantile(0.99,sum(rest_client_request_duration_seconds_bucket{url=~"%s"})by(url,verb,le))`
)

type ResourceCollector struct {
	Token                 string
	OnOCP                 bool
	VirtClient            kubecli.KubevirtClient
	Op                    k8sv1.Pod
	ServicePrometheusPort int32
	Client                prom_v1.API
}

type Agg string

const (
	SUM Agg = "sum"
	AVG Agg = "avg"
)

// SetVirtClient set the cluster client
func (rc *ResourceCollector) SetVirtClient(virtClient kubecli.KubevirtClient) {
	rc.VirtClient = virtClient
}

// NewPromClient initialize a new client to the Prometheus API.
func (rc *ResourceCollector) NewPromClient() {
	rt := prom_api.DefaultRoundTripper.(*http.Transport)

	clientConfig := prom_api.Config{}

	clientConfig.RoundTripper = &authRoundTripper{token: rc.Token, originalRT: rt}
	clientConfig.Address = fmt.Sprintf("http://%s:%d", localPrometheusEndpoint, localPrometheusPort)

	p8s, _ := prom_api.NewClient(clientConfig)

	rc.Client = prom_v1.NewAPI(p8s)
}

// // SetPrometheusPort finds the Prometheus URL and Port
func (rc *ResourceCollector) SetPrometheusPort() {
	var ep *k8sv1.Endpoints

	// finding Prometheus endpoint
	Eventually(func() bool {
		var err error
		ep, err = rc.VirtClient.CoreV1().Endpoints(namespace).Get(context.Background(), prometheusService, metav1.GetOptions{})
		Expect(err).ToNot(HaveOccurred(), "failed to retrieve Prometheus endpoint")

		if len(ep.Subsets) == 0 || len(ep.Subsets[0].Addresses) == 0 {
			return false
		}
		return true
	}, 10*time.Second, time.Second).Should(BeTrue())

	for _, port := range ep.Subsets[0].Ports {
		if port.Name == "web" {
			rc.ServicePrometheusPort = port.Port
		}
	}
	Expect(rc.ServicePrometheusPort).ToNot(Equal(0), "could not get Prometheus port from endpoint")
}

// SetToken returns true if the token was found, otherwise, it means that Prometheus could not be found
func (rc *ResourceCollector) SetToken() bool {
	secrets, err := rc.VirtClient.CoreV1().Secrets(namespace).List(context.Background(), metav1.ListOptions{})
	Expect(err).ToNot(HaveOccurred())
	for _, secret := range secrets.Items {
		if strings.Contains(secret.Name, prometheusSecret) {
			token := string(secret.Data["token"])
			rc.Token = "Bearer " + token
			return true
		}
	}
	return false
}

// CreatePromPortForward set up port forwarding for Prometheus service")
func (rc *ResourceCollector) CreatePromPortForward() {
	portMapping := fmt.Sprintf("%d:%d", localPrometheusPort, rc.ServicePrometheusPort)
	_, kubectlCmd, err := tests.CreateCommandWithNS(namespace, "kubectl", "port-forward", "service/"+prometheusService, portMapping)
	Expect(err).ToNot(HaveOccurred())

	err = kubectlCmd.Start()
	Expect(err).ToNot(HaveOccurred())
}

// Query queries Prometheus API endpoint for a given metric
func (rc *ResourceCollector) Query(query string) (prom_model.Value, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	res, warnings, err := rc.Client.Query(ctx, query, time.Now())
	if err != nil {
		fmt.Printf("FATAL: Error querying Prometheus: %v\n", err)
	}
	if len(warnings) > 0 {
		fmt.Printf("WARN: %v\n", warnings)
	}
	return res, err
}

// Query queries Prometheus API endpoint for a given metric
func (rc *ResourceCollector) QueryToValues(sample prom_model.Value) []float64 {
	var values []float64
	for _, s := range sample.(model.Vector) {
		values = append(values, float64(s.Value))
	}
	return values
}

// Query queries Prometheus API endpoint for a given metric
func (rc *ResourceCollector) QueryToValuesAgg(sample prom_model.Value, agg Agg) float64 {
	var values []float64
	for _, s := range sample.(model.Vector) {
		values = append(values, float64(s.Value))
	}

	total := 0.0
	for _, v := range values {
		total += v
	}
	if agg == AVG {
		return total / float64(len(values))
	}
	return total
}

// GetApiRequestDuration queries Prometheus API endpoint for the kubevirt API REST request duration, it returns the 99-percentile of the latencies of APIS
func (rc *ResourceCollector) GetApiRequestDuration(url string) (prom_model.Value, error) {
	query := fmt.Sprintf(
		apiRequestDuration,
		fmt.Sprintf(`.*%s.*`, url),
	)
	return rc.Query(query)
}

// GetVMIMemory queries Prometheus API endpoint for the VMI memory usage
func (rc *ResourceCollector) GetVMIMemory(vmi *apiv1.VirtualMachineInstance) (prom_model.Value, error) {
	query := fmt.Sprintf(
		vmiMEMQuery,
		vmi.Namespace,
		vmi.Name,
	)
	return rc.Query(query)
}

// GetVMICPU queries Prometheus API endpoint for the VMI memory usage
func (rc *ResourceCollector) GetVMICPU(vmi *apiv1.VirtualMachineInstance) (prom_model.Value, error) {
	query := fmt.Sprintf(
		vmiCPUQuery,
		vmi.Namespace,
		vmi.Name,
	)
	return rc.Query(query)
}

// GetVMIPodMemory queries Prometheus API endpoint for the VMI pod memory usage, returning the aggregation of all containers
func (rc *ResourceCollector) GetVMIPodMemory(vmi *apiv1.VirtualMachineInstance) (prom_model.Value, error) {
	pod := rc.GetVMIPod(vmi)
	return rc.GetPodMemory(pod.Namespace, pod.Name)
}

// GetVMIPodCPU queries Prometheus API endpoint for the VMI pod CPU usage, returning the aggregation of all containers
func (rc *ResourceCollector) GetVMIPodCPU(vmi *apiv1.VirtualMachineInstance) (prom_model.Value, error) {
	pod := rc.GetVMIPod(vmi)
	return rc.GetPodCPU(pod.Namespace, pod.Name)
}

// GetPodMemory queries Prometheus API endpoint for the pod memory usage of all pods in a given namespace
func (rc *ResourceCollector) GetPodMemory(namespace string, pod string) (prom_model.Value, error) {
	query := fmt.Sprintf(
		podMEMQuery,
		fmt.Sprintf(`namespace=~"%s.*",pod=~"%s.*"`,
			namespace,
			pod,
		),
	)
	return rc.Query(query)
}

// GetPodCPU queries Prometheus API endpoint for the pod CPU usage of all pods in a given namespace
func (rc *ResourceCollector) GetPodCPU(namespace string, pod string) (prom_model.Value, error) {
	query := fmt.Sprintf(
		podCPUQuery,
		fmt.Sprintf(`namespace=~"%s.*",pod=~"%s.*"`,
			namespace,
			pod,
		),
	)
	return rc.Query(query)
}

// GetVMIPod returns the VMI virt-launcher pod
func (rc *ResourceCollector) GetVMIPod(vmi *apiv1.VirtualMachineInstance) k8sv1.Pod {
	pods, err := rc.VirtClient.CoreV1().Pods(vmi.Namespace).List(context.Background(), metav1.ListOptions{
		LabelSelector: apiv1.CreatedByLabel + "=" + string(vmi.GetUID()),
	})
	Expect(err).ToNot(HaveOccurred(), "should list pods successfully")
	Expect(pods.Size).ToNot(Equal(0), "no virt-launcher found")
	pod := pods.Items[0]
	Expect(pod).ToNot(BeNil(), "virt-launcher pod should not be nil")
	return pod
}

// Create and initialize a new Resource Collector
func Create(virtClient *kubecli.KubevirtClient) *ResourceCollector {
	if virtClient == nil {
		fmt.Printf("Cannot create a Resource Collector without a KubevirtClient (%v)", virtClient)
		return nil
	}

	rc := ResourceCollector{}
	rc.SetVirtClient(*virtClient)
	if hasPrometheus := rc.SetToken(); hasPrometheus {
		rc.NewPromClient()
		rc.SetPrometheusPort()
		rc.CreatePromPortForward()
		return &rc
	}

	return nil
}
