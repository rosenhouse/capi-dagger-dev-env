// Package v1alpha1 contains the demo.example.com API.
// +kubebuilder:object:generate=true
// +groupName=demo.example.com
package v1alpha1

import (
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/scheme"
)

//go:generate go tool controller-gen object crd paths=./... output:crd:dir=../../config/crd

var (
	GroupVersion  = schema.GroupVersion{Group: "demo.example.com", Version: "v1alpha1"}
	SchemeBuilder = &scheme.Builder{GroupVersion: GroupVersion}
	AddToScheme   = SchemeBuilder.AddToScheme
)
