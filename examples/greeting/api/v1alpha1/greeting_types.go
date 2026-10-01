package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// +kubebuilder:object:root=true
// +kubebuilder:printcolumn:name="Cluster",type=string,JSONPath=`.spec.clusterName`
// +kubebuilder:printcolumn:name="Message",type=string,JSONPath=`.spec.message`

// Greeting is a message served by the hello app in a workload cluster.
type Greeting struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec GreetingSpec `json:"spec"`
}

type GreetingSpec struct {
	// ClusterName is the CAPI Cluster, in this namespace, that receives a copy.
	// It is set only in the management cluster.
	// +optional
	ClusterName string `json:"clusterName,omitempty"`

	// +kubebuilder:validation:MinLength=1
	Message string `json:"message"`
}

// +kubebuilder:object:root=true

type GreetingList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Greeting `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Greeting{}, &GreetingList{})
}
