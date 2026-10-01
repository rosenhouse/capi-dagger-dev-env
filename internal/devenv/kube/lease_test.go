package kube_test

import (
	"context"
	"testing"
	"time"

	coordinationv1 "k8s.io/api/coordination/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/rosenhouse/capi-dagger-dev-env/internal/devenv/kube"
)

var since = time.Date(2026, 10, 1, 10, 0, 0, 0, time.UTC)

func TestLeasesRenewedSince(t *testing.T) {
	sets := []kube.Leases{{Namespace: "kube-node-lease"}, {Namespace: "kube-system", Names: []string{"kube-scheduler"}}}
	for _, tc := range []struct {
		name   string
		leases []runtime.Object
		want   string
	}{{
		name: "renewed",
		leases: []runtime.Object{
			lease("kube-node-lease", "a", "a", since), lease("kube-node-lease", "b", "b", since.Add(time.Second)),
			lease("kube-system", "kube-scheduler", "s", since), lease("kube-system", "unnamed", "u", since.Add(-time.Hour)),
		},
	}, {
		name: "a lease without a holder",
		leases: []runtime.Object{
			lease("kube-node-lease", "a", "a", since), lease("kube-node-lease", "released", "", since.Add(-time.Hour)),
			lease("kube-system", "kube-scheduler", "s", since),
		},
	}, {
		name: "stale",
		leases: []runtime.Object{
			lease("kube-node-lease", "a", "a", since.Add(-time.Millisecond)), lease("kube-node-lease", "b", "b", since),
			lease("kube-system", "kube-scheduler", "s", since.Add(-time.Second)),
		},
		want: "leases not renewed since 10:00:00.000: kube-node-lease/a, kube-system/kube-scheduler",
	}, {
		name:   "a named lease is missing",
		leases: []runtime.Object{lease("kube-node-lease", "a", "a", since)},
		want:   `leases.coordination.k8s.io "kube-scheduler" not found`,
	}, {
		name:   "a namespace has no held leases",
		leases: []runtime.Object{lease("kube-node-lease", "a", "", since), lease("kube-system", "kube-scheduler", "s", since)},
		want:   "no held leases in kube-node-lease",
	}} {
		t.Run(tc.name, func(t *testing.T) {
			err := kube.LeasesRenewedSince(context.Background(), fake.NewClientset(tc.leases...), since, sets...)

			if got := errString(err); got != tc.want {
				t.Errorf("err = %q; want %q", got, tc.want)
			}
		})
	}
}

func lease(namespace, name, holder string, renewed time.Time) runtime.Object {
	l := &coordinationv1.Lease{ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name}}
	if holder != "" {
		l.Spec.HolderIdentity = &holder
	}
	renewTime := metav1.NewMicroTime(renewed)
	l.Spec.RenewTime = &renewTime
	return l
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
