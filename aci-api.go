// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
// This program is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
// GNU General Public License for more details.
// You should have received a copy of the GNU General Public License
// along with this program.  If not, see <https://www.gnu.org/licenses/>.
//
// Copyright 2020 Opsdis AB

package main

import (
	log "github.com/sirupsen/logrus"
	"github.com/spf13/viper"
	"github.com/tidwall/gjson"
	"regexp"
	"strconv"
	"time"
)

var re_health = regexp.MustCompile("topology/pod-(.*?)/health")

//var re_podId = regexp.MustCompile("pod-(.*?)")

func newAciAPI(apichostname string, username string, password string) *aciAPI {

	api := &aciAPI{
		connection:   *newAciConnction(apichostname, username, password),
		metricPrefix: viper.GetString("prefix"),
	}

	return api
}

type aciAPI struct {
	connection    AciConnection
	batchFetch    bool
	batchFilter   string
	batchInterval int
	metricPrefix  string
}

// CollectMetrics Gather all aci metrics and return name of the aci fabric, slice of metrics and status of
// successful login
func (p aciAPI) CollectMetrics() (string, []MetricDefinition, bool) {
	start := time.Now()

	status := p.connection.login()
	defer p.connection.logout()

	if !status {
		return "", nil, status
	}

	fabricName := p.getFabricName()

	// Hold all metrics created during the session
	metrics := []MetricDefinition{}

	metrics = append(metrics, p.fabricHealth()...)
	metrics = append(metrics, *p.nodeHealth())
	metrics = append(metrics, *p.tenantHealth())
	metrics = append(metrics, p.faults()...)
	metrics = append(metrics, *p.infraNodeInfo())

	// Todo EPG health

	metrics = append(metrics, *p.scrape(time.Since(start).Seconds()))

	return fabricName, metrics, true
}

func (p aciAPI) scrape(seconds float64) *MetricDefinition {
	metricDefinition := MetricDefinition{}
	metricDefinition.Name = "scrape_duration"
	metricDefinition.Description = MetricDesc{
		Help: "The duration, in seconds, of the last scrape of the fabric",
		Type: "gauge",
		Unit: "seconds",
	}
	metricDefinition.Metrics = []Metric{}

	metric := Metric{}
	metric.Labels = make(map[string]string)
	metric.Value = seconds

	metricDefinition.Metrics = append(metricDefinition.Metrics, metric)

	return &metricDefinition
}

func (p aciAPI) fabricHealth() []MetricDefinition {
	data, err := p.connection.getByQuery("fabric_health")
	if err != nil {
		log.Error("fabric_health not supported", err)
		return nil
	}

	metricDefinitionOverall := MetricDefinition{}
	metricDefinitionOverall.Name = "fabric_health_overall"
	metricDefinitionOverall.Description = MetricDesc{
		Help: "Returns the health score of the overall fabric",
		Type: "gauge",
		Unit: "ratio",
	}
	metricDefinitionOverall.Metrics = []Metric{}

	metricDefinitionPod := MetricDefinition{}
	metricDefinitionPod.Name = "pod_health"
	metricDefinitionPod.Description = MetricDesc{
		Help: "Returns the health score of a pod",
		Type: "gauge",
		Unit: "ratio",
	}
	metricDefinitionPod.Metrics = []Metric{}

	result := gjson.Get(data, "imdata")

	result.ForEach(func(key, value gjson.Result) bool {
		dn := gjson.Get(value.String(), "fabricHealthTotal.attributes.dn")

		metric := Metric{}

		match := re_health.FindStringSubmatch(dn.Str)
		if len(match) == 0 {
			metric.Labels = make(map[string]string)

			metric.Value = p.toRatio(gjson.Get(value.String(), "fabricHealthTotal.attributes.cur").Str)
			metricDefinitionOverall.Metrics = append(metricDefinitionOverall.Metrics, metric)
		} else {
			metric.Labels = make(map[string]string)
			metric.Labels["podid"] = match[1]

			metric.Value = p.toRatio(gjson.Get(value.String(), "fabricHealthTotal.attributes.cur").Str)

			metricDefinitionPod.Metrics = append(metricDefinitionPod.Metrics, metric)
		}
		return true
	})

	return []MetricDefinition{metricDefinitionOverall, metricDefinitionPod}
}

// nodeHealth only leaf and spine nodes
func (p aciAPI) nodeHealth() *MetricDefinition {
	data, err := p.connection.getByQuery("node_health")
	if err != nil {
		log.Error("node_health not supported", err)
		return nil
	}

	metricDefinition := MetricDefinition{}
	metricDefinition.Name = "node_health"

	metricDefinition.Description = MetricDesc{
		Help: "Returns the health score of a fabric node",
		Type: "gauge",
		Unit: "ratio",
	}

	metrics := []Metric{}
	result := gjson.Get(data, "imdata")

	result.ForEach(func(key, value gjson.Result) bool {
		role := gjson.Get(value.String(), "topSystem.attributes.role").Str
		metric := Metric{}

		if role != "controller" {

			metric.Labels = make(map[string]string)
			metric.Labels["podid"] = gjson.Get(value.String(), "topSystem.attributes.podId").Str
			metric.Labels["state"] = gjson.Get(value.String(), "topSystem.attributes.state").Str
			metric.Labels["oobmgmtaddr"] = gjson.Get(value.String(), "topSystem.attributes.oobMgmtAddr").Str
			metric.Labels["nodeid"] = gjson.Get(value.String(), "topSystem.attributes.id").Str
			metric.Labels["name"] = gjson.Get(value.String(), "topSystem.attributes.name").Str
			metric.Labels["role"] = role

			metric.Value = p.toRatio(gjson.Get(value.String(), "topSystem.children.0.healthInst.attributes.cur").Str)

			metrics = append(metrics, metric)
		}
		return true // keep iterating
	})

	metricDefinition.Metrics = metrics

	return &metricDefinition
}

func (p aciAPI) tenantHealth() *MetricDefinition {
	data, err := p.connection.getByQuery("tenant_health")
	if err != nil {
		log.Error("tenant_health not supported", err)
		return nil
	}

	metricDefinition := MetricDefinition{}
	metricDefinition.Name = "tenant_health"
	metricDefinition.Description = MetricDesc{
		Help: "Returns the health score of a tenant",
		Type: "gauge",
		Unit: "ratio",
	}

	metrics := []Metric{}

	result := gjson.Get(data, "imdata")

	result.ForEach(func(key, value gjson.Result) bool {

		metric := Metric{}

		metric.Labels = make(map[string]string)
		metric.Labels["domain"] = gjson.Get(value.String(), "fvTenant.attributes.name").Str

		metric.Value = p.toRatio(gjson.Get(value.String(), "fvTenant.children.0.healthInst.attributes.cur").Str)

		metrics = append(metrics, metric)

		return true // keep iterating
	})

	metricDefinition.Metrics = metrics
	return &metricDefinition
}

func (p aciAPI) faults() []MetricDefinition {
	data, err := p.connection.getByQuery("faults")
	if err != nil {
		log.Error("faults not supported", err)
		return nil
	}

	metricDefinitionFaults := MetricDefinition{}
	metricDefinitionFaults.Name = "faults"
	metricDefinitionFaults.Description = MetricDesc{
		Help: "Returns the total number of faults by type",
		Type: "gauge",
		Unit: "",
	}

	metrics := []Metric{}
	children := gjson.Get(data, "imdata.0.faultCountsWithDetails.children.#.faultTypeCounts")

	children.ForEach(func(key, value gjson.Result) bool {

		metric := Metric{}
		metric.Labels = make(map[string]string)
		metric.Labels["type"] = gjson.Get(value.String(), "attributes.type").Str
		metric.Labels["severity"] = "crit"
		metric.Value = p.toFloat(gjson.Get(value.String(), "attributes.crit").Str)
		metrics = append(metrics, metric)

		metric = Metric{}
		metric.Labels = make(map[string]string)
		metric.Labels["type"] = gjson.Get(value.String(), "attributes.type").Str
		metric.Labels["severity"] = "maj"
		metric.Value = p.toFloat(gjson.Get(value.String(), "attributes.maj").Str)
		metrics = append(metrics, metric)

		metric = Metric{}
		metric.Labels = make(map[string]string)
		metric.Labels["type"] = gjson.Get(value.String(), "attributes.type").Str
		metric.Labels["severity"] = "minor"
		metric.Value = p.toFloat(gjson.Get(value.String(), "attributes.minor").Str)
		metrics = append(metrics, metric)

		metric = Metric{}
		metric.Labels = make(map[string]string)
		metric.Labels["type"] = gjson.Get(value.String(), "attributes.type").Str
		metric.Labels["severity"] = "warn"
		metric.Value = p.toFloat(gjson.Get(value.String(), "attributes.warn").Str)
		metrics = append(metrics, metric)

		return true // keep iterating
	})

	metricDefinitionFaults.Metrics = metrics

	metrics = []Metric{}
	metricDefinitionAcked := MetricDefinition{}
	metricDefinitionAcked.Name = "faults_acked"
	metricDefinitionAcked.Description = MetricDesc{
		Help: "Returns the total number of acknowledged faults by type",
		Type: "gauge",
		Unit: "",
	}

	children.ForEach(func(key, value gjson.Result) bool {

		metric := Metric{}
		metric.Labels = make(map[string]string)
		metric.Labels["type"] = gjson.Get(value.String(), "attributes.type").Str
		metric.Labels["severity"] = "crit"
		metric.Value = p.toFloat(gjson.Get(value.String(), "attributes.critAcked").Str)
		metrics = append(metrics, metric)

		metric = Metric{}
		metric.Labels = make(map[string]string)
		metric.Labels["type"] = gjson.Get(value.String(), "attributes.type").Str
		metric.Labels["severity"] = "maj"
		metric.Value = p.toFloat(gjson.Get(value.String(), "attributes.majAcked").Str)
		metrics = append(metrics, metric)

		metric = Metric{}
		metric.Labels = make(map[string]string)
		metric.Labels["type"] = gjson.Get(value.String(), "attributes.type").Str
		metric.Labels["severity"] = "minor"
		metric.Value = p.toFloat(gjson.Get(value.String(), "attributes.minorAcked").Str)
		metrics = append(metrics, metric)

		metric = Metric{}
		metric.Labels = make(map[string]string)
		metric.Labels["type"] = gjson.Get(value.String(), "attributes.type").Str
		metric.Labels["severity"] = "warn"
		metric.Value = p.toFloat(gjson.Get(value.String(), "attributes.warnAcked").Str)
		metrics = append(metrics, metric)

		return true // keep iterating
	})

	metricDefinitionAcked.Metrics = metrics

	return []MetricDefinition{metricDefinitionFaults, metricDefinitionAcked}
}

func (p aciAPI) infraNodeInfo() *MetricDefinition {
	data, err := p.connection.getByQuery("infra_node_health")
	if err != nil {
		log.Error("infra_node_health not supported", err)
		return nil
	}

	metricDefinition := MetricDefinition{}
	metricDefinition.Name = "infra_node"

	metricDefinition.Description = MetricDesc{
		Help: "Returns the info of the infrastructure apic node",
		Type: "counter",
		Unit: "info",
	}

	metrics := []Metric{}
	result := gjson.Get(data, "imdata")

	result.ForEach(func(key, value gjson.Result) bool {

		metric := Metric{}

		metric.Labels = make(map[string]string)
		metric.Labels["name"] = gjson.Get(value.String(), "infraWiNode.attributes.nodeName").Str
		metric.Labels["address"] = gjson.Get(value.String(), "infraWiNode.attributes.addr").Str
		metric.Labels["health"] = gjson.Get(value.String(), "infraWiNode.attributes.health").Str
		metric.Labels["apicmode"] = gjson.Get(value.String(), "infraWiNode.attributes.apicMode").Str
		metric.Labels["adminstatus"] = gjson.Get(value.String(), "infraWiNode.attributes.adminSt").Str
		metric.Labels["operstatus"] = gjson.Get(value.String(), "infraWiNode.attributes.operSt").Str
		metric.Labels["failoverStatus"] = gjson.Get(value.String(), "infraWiNode.attributes.failoverStatus").Str
		metric.Labels["podid"] = gjson.Get(value.String(), "infraWiNode.attributes.podId").Str

		metric.Value = 1.0

		metrics = append(metrics, metric)

		return true
	})

	metricDefinition.Metrics = metrics
	return &metricDefinition
}

func (p aciAPI) getFabricName() string {
	data, err := p.connection.getByQuery("fabric_name")
	if err != nil {
		log.Error("fabric_health not supported", err)
		return ""
	}

	return gjson.Get(data, "imdata.0.infraCont.attributes.fbDmNm").Str
}

func (p aciAPI) toRatio(value string) float64 {
	rate, _ := strconv.ParseFloat(value, 64)
	return rate / 100.0
}

func (p aciAPI) toFloat(value string) float64 {
	rate, _ := strconv.ParseFloat(value, 64)
	return rate
}
