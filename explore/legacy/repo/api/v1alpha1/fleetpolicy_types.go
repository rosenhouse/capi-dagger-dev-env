package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

type FleetPolicySpec struct {
	// Message is copied into every managed workload cluster.
	Message string `json:"message"`
}

type FleetPolicyStatus struct {
	Clusters int `json:"clusters,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
type FleetPolicy struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   FleetPolicySpec   `json:"spec,omitempty"`
	Status FleetPolicyStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type FleetPolicyList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []FleetPolicy `json:"items"`
}

func init() {
	SchemeBuilder.Register(&FleetPolicy{}, &FleetPolicyList{})
}
