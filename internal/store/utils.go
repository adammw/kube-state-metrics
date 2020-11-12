/*
Copyright 2018 The Kubernetes Authors All rights reserved.

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

package store

import (
	"fmt"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"regexp"
	"sort"
	"strconv"
	"strings"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/validation"

	"k8s.io/kube-state-metrics/v2/pkg/allow"
	"k8s.io/kube-state-metrics/v2/pkg/metric"
)

var (
	invalidLabelCharRE = regexp.MustCompile(`[^a-zA-Z0-9_]`)
	matchAllCap        = regexp.MustCompile("([a-z0-9])([A-Z])")
	conditionStatuses  = []v1.ConditionStatus{v1.ConditionTrue, v1.ConditionFalse, v1.ConditionUnknown}
	knownConditionReasons = map[schema.GroupVersionKind]map[v1.ConditionStatus]map[string]bool{}
)

func resourceVersionMetric(rv string) []*metric.Metric {
	v, err := strconv.ParseFloat(rv, 64)
	if err != nil {
		return []*metric.Metric{}
	}

	return []*metric.Metric{
		{
			Value: v,
		},
	}

}

func boolFloat64(b bool) float64 {
	if b {
		return 1
	}
	return 0
}

// addConditionMetrics generates one metric for each possible condition
// status and known reason for that object type. this has to grow dynamically as the values of `reason`,
// while bounded by the values that the implementation can set, are not exported and known before-hand.
//
// TODO: confirm statement - For this function to work properly, the last label in the metric
// description must be the condition.
func addConditionMetrics(gvk schema.GroupVersionKind, cs v1.ConditionStatus, reason string) []*metric.Metric {
	// create map if not initialized
	if _, ok := knownConditionReasons[gvk]; !ok {
		knownConditionReasons[gvk] = map[v1.ConditionStatus]map[string]bool{}
	}

	knownReasonCount := 0
	for _, status := range conditionStatuses {
		// create map if not initialized
		if _, ok := knownConditionReasons[gvk][status]; !ok {
			knownConditionReasons[gvk][status] = map[string]bool{}
		}

		// add current reason to list of known reasons
		knownConditionReasons[gvk][status][reason] = true

		knownReasonCount += len(knownConditionReasons[gvk][status])
	}

	ms := make([]*metric.Metric, knownReasonCount)
	i := 0
	for _, status := range conditionStatuses {
		for knownReason, _ := range knownConditionReasons[gvk][status] {
			ms[i] = &metric.Metric{
				LabelKeys:   []string{"status", "reason"},
				LabelValues: []string{strings.ToLower(string(status)), strings.ToLower(knownReason)},
				Value:       boolFloat64(cs == status && reason == knownReason),
			}
			i++
		}
	}

	return ms
}

func sanitizeAllowLabels(l map[string][]string) allow.Labels {
	allowLabels := make(map[string][]string)
	for m, labels := range l {
		allowLabels[sanitizeLabelName(m)] = labels
	}
	return allowLabels
}

func kubeLabelsToPrometheusLabels(labels map[string]string) ([]string, []string) {
	return mapToPrometheusLabels(labels, "label")
}

func mapToPrometheusLabels(labels map[string]string, prefix string) ([]string, []string) {
	labelKeys := make([]string, 0, len(labels))
	labelValues := make([]string, 0, len(labels))

	sortedKeys := make([]string, 0)
	for key := range labels {
		sortedKeys = append(sortedKeys, key)
	}
	sort.Strings(sortedKeys)

	// conflictDesc holds some metadata for resolving potential label conflicts
	type conflictDesc struct {
		// the number of conflicting label keys we saw so far
		count int

		// the offset of the initial conflicting label key, so we could
		// later go back and rename "label_foo" to "label_foo_conflict1"
		initial int
	}

	conflicts := make(map[string]*conflictDesc)
	for _, k := range sortedKeys {
		labelKey := labelName(prefix, k)
		if conflict, ok := conflicts[labelKey]; ok {
			if conflict.count == 1 {
				// this is the first conflict for the label,
				// so we have to go back and rename the initial label that we've already added
				labelKeys[conflict.initial] = labelConflictSuffix(labelKeys[conflict.initial], conflict.count)
			}

			conflict.count++
			labelKey = labelConflictSuffix(labelKey, conflict.count)
		} else {
			// we'll need this info later in case there are conflicts
			conflicts[labelKey] = &conflictDesc{
				count:   1,
				initial: len(labelKeys),
			}
		}
		labelKeys = append(labelKeys, labelKey)
		labelValues = append(labelValues, labels[k])
	}
	return labelKeys, labelValues
}

func labelName(prefix, labelName string) string {
	return prefix + "_" + lintLabelName(sanitizeLabelName(labelName))
}

func sanitizeLabelName(s string) string {
	return invalidLabelCharRE.ReplaceAllString(s, "_")
}

func lintLabelName(s string) string {
	return toSnakeCase(s)
}

func toSnakeCase(s string) string {
	snake := matchAllCap.ReplaceAllString(s, "${1}_${2}")
	return strings.ToLower(snake)
}

func labelConflictSuffix(label string, count int) string {
	return fmt.Sprintf("%s_conflict%d", label, count)
}

func isHugePageResourceName(name v1.ResourceName) bool {
	return strings.HasPrefix(string(name), v1.ResourceHugePagesPrefix)
}

func isAttachableVolumeResourceName(name v1.ResourceName) bool {
	return strings.HasPrefix(string(name), v1.ResourceAttachableVolumesPrefix)
}

func isExtendedResourceName(name v1.ResourceName) bool {
	if isNativeResource(name) || strings.HasPrefix(string(name), v1.DefaultResourceRequestsPrefix) {
		return false
	}
	// Ensure it satisfies the rules in IsQualifiedName() after converted into quota resource name
	nameForQuota := fmt.Sprintf("%s%s", v1.DefaultResourceRequestsPrefix, string(name))
	if errs := validation.IsQualifiedName(nameForQuota); len(errs) != 0 {
		return false
	}
	return true
}

func isNativeResource(name v1.ResourceName) bool {
	return !strings.Contains(string(name), "/") ||
		isPrefixedNativeResource(name)
}

func isPrefixedNativeResource(name v1.ResourceName) bool {
	return strings.Contains(string(name), v1.ResourceDefaultNamespacePrefix)
}
