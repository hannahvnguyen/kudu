/*
Copyright 2021.

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

package v1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// KuduClusterSpec defines the desired state of KuduCluster
type KuduClusterSpec struct {
	// +kubebuilder:validation:Minimum=0
	// NumMasters is the number of the masters in the KuduCluster
	NumMasters int32 `json:"num-masters"`

	// +kubebuilder:validation:Minimum=0
	// NumTservers is the number of the tservers in the KuduCluster
	NumTservers int32 `json:"num-tservers"`
}

// KuduClusterStatus defines the observed state of KuduCluster
type KuduClusterStatus struct {
	// KuduMasters are the names of the masters in the KuduCluster
	KuduMasters []string `json:"kudu-masters"`

	// KuduTservers are the names of the tservers in the KuduCluster
	KuduTservers []string `json:"kudu-tservers"`
}

//+kubebuilder:object:root=true
//+kubebuilder:subresource:status

// KuduCluster is the Schema for the kuduclusters API
type KuduCluster struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   KuduClusterSpec   `json:"spec,omitempty"`
	Status KuduClusterStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// KuduClusterList contains a list of KuduCluster
type KuduClusterList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []KuduCluster `json:"items"`
}

func init() {
	SchemeBuilder.Register(&KuduCluster{}, &KuduClusterList{})
}
