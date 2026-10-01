package kube_test

import (
	"context"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	"github.com/rosenhouse/capi-dagger-dev-env/internal/devenv/kube"
)

func TestMachinesHealthy(t *testing.T) {
	for _, tc := range []struct {
		name string
		mhcs []runtime.Object
		want string
	}{{
		name: "healthy",
		mhcs: []runtime.Object{mhc("work-cp", map[string]any{"expectedMachines": int64(1), "currentHealthy": int64(1)}),
			mhc("work-md-0", map[string]any{"expectedMachines": int64(2), "currentHealthy": int64(2)})},
	}, {
		name: "none",
	}, {
		name: "unhealthy",
		mhcs: []runtime.Object{mhc("work-cp", map[string]any{"expectedMachines": int64(1), "currentHealthy": int64(1)}),
			mhc("work-md-0", map[string]any{"expectedMachines": int64(2), "currentHealthy": int64(1)})},
		want: "MachineHealthCheck default/work-md-0 counts 1 of 2 machines healthy",
	}, {
		name: "not yet counted",
		mhcs: []runtime.Object{mhc("work-cp", map[string]any{})},
		want: "MachineHealthCheck default/work-cp has not counted its machines",
	}} {
		t.Run(tc.name, func(t *testing.T) {
			dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
				map[schema.GroupVersionResource]string{kube.MachineHealthCheckGVR: "MachineHealthCheckList"}, tc.mhcs...)

			err := kube.MachinesHealthy(context.Background(), dyn, "default")

			if got := errString(err); got != tc.want {
				t.Errorf("err = %q; want %q", got, tc.want)
			}
		})
	}
}

func mhc(name string, status map[string]any) runtime.Object {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "cluster.x-k8s.io/v1beta2",
		"kind":       "MachineHealthCheck",
		"metadata":   map[string]any{"name": name, "namespace": "default"},
		"status":     status,
	}}
}
