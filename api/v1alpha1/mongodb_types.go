/*
Copyright 2022.

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

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// MongoDBSpec defines the desired state of MongoDB
type MongoDBSpec struct {
	// Port is the port number exposed in the service; if not set, the service port will be 27017
	Port int32 `json:"port,omitempty"`

	// StorageClassName for the PVC; if missing, the StorageClass will be the cluster default
	StorageClassName *string `json:"storageClassName,omitempty"`

	// Replicas is the desired number of nodes in the mongodb replica set
	// +kubebuilder:default:=3
	// +kubebuilder:validation:Enum=3;5;7;9
	Replicas *int32 `json:"replicas,omitempty"`
}

// MongoDBStatus defines the observed state of MongoDB
type MongoDBStatus struct {
	// Conditions tells the current status of the resource
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// ConnectionURL may be used by a client to connetc to the mongoDB replica set
	ConnectionURL *string `json:"connectionURL,omitempty"`

	// Replicas is the actual number of replicas in the mongoDB replica set
	Replicas *int32 `json:"replicas,omitempty"`
}

// +kubebuilder:resource:shortName=mdb
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:subresource:scale:specpath=.spec.replicas,statuspath=.status.replicas
// +kubebuilder:printcolumn:name="Replicas",type="integer",JSONPath=".status.replicas",description="Number of hosts in the mongoDB replicaSet"

// MongoDB is the Schema for the mongodbs API
type MongoDB struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   MongoDBSpec   `json:"spec,omitempty"`
	Status MongoDBStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// MongoDBList contains a list of MongoDB
type MongoDBList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []MongoDB `json:"items"`
}

func init() {
	SchemeBuilder.Register(&MongoDB{}, &MongoDBList{})
}
