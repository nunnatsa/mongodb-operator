/*
Copyright 2024.

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

// EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!
// NOTE: json tags are required.  Any new fields you add must have json tags for the fields to be serialized.

// MongoDBReplicaSetSpec defines the desired state of MongoDBReplicaSet
type MongoDBReplicaSetSpec struct {
	// Port is the port number exposed in the service; default = 27017
	// +kubebuilder:default:=27017
	// +kubebuilder:validation:XValidation:rule="self == oldSelf", message="Port is immutable"
	Port int32 `json:"port,omitempty"`

	// StorageClassName for the PVC; if missing, the StorageClass will be the cluster default
	StorageClassName *string `json:"storageClassName,omitempty"`

	// Replicas is the desired number of nodes in the mongodb replica set
	// +kubebuilder:default:=3
	// +kubebuilder:validation:Enum=3;5;7;9
	Replicas *int32 `json:"replicas,omitempty"`
}

// MongoDBReplicaSetStatus defines the observed state of MongoDBReplicaSet
type MongoDBReplicaSetStatus struct {
	// Conditions tells the current status of the resource
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// ConnectionURL may be used by a client to connect to the mongoDB replica set
	ConnectionURL *string `json:"connectionURL,omitempty"`

	// Replicas is the actual number of replicas in the mongoDB replica set
	Replicas *int32 `json:"replicas,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=mrs
// +kubebuilder:subresource:scale:specpath=.spec.replicas,statuspath=.status.replicas
// +kubebuilder:printcolumn:name="Replicas",type="integer",JSONPath=".status.replicas",description="Number of hosts in the mongoDB replicaSet"

// MongoDBReplicaSet is the Schema for the mongodbreplicasets API
type MongoDBReplicaSet struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   MongoDBReplicaSetSpec   `json:"spec,omitempty"`
	Status MongoDBReplicaSetStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// MongoDBReplicaSetList contains a list of MongoDBReplicaSet
type MongoDBReplicaSetList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []MongoDBReplicaSet `json:"items"`
}

func init() {
	SchemeBuilder.Register(&MongoDBReplicaSet{}, &MongoDBReplicaSetList{})
}
