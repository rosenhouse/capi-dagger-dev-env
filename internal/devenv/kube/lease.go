package kube

import (
	"context"
	"fmt"
	"strings"
	"time"

	coordinationv1 "k8s.io/api/coordination/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// Leases names Leases in Namespace: Names, or without them every Lease there that has a holder.
type Leases struct {
	Namespace string
	Names     []string
}

// LeasesRenewedSince returns nil when every Lease that leases name was renewed at or after since.
func LeasesRenewedSince(ctx context.Context, cs kubernetes.Interface, since time.Time, leases ...Leases) error {
	var stale []string
	for _, set := range leases {
		held, err := set.get(ctx, cs)
		if err != nil {
			return err
		}
		for _, l := range held {
			if l.Spec.RenewTime == nil || l.Spec.RenewTime.Time.Before(since) {
				stale = append(stale, l.Namespace+"/"+l.Name)
			}
		}
	}
	if len(stale) > 0 {
		return fmt.Errorf("leases not renewed since %s: %s", since.Format("15:04:05.000"), strings.Join(stale, ", "))
	}
	return nil
}

func (set Leases) get(ctx context.Context, cs kubernetes.Interface) ([]coordinationv1.Lease, error) {
	client := cs.CoordinationV1().Leases(set.Namespace)
	var leases []coordinationv1.Lease
	for _, name := range set.Names {
		l, err := client.Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return nil, err
		}
		leases = append(leases, *l)
	}
	if len(set.Names) > 0 {
		return leases, nil
	}
	list, err := client.List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	for _, l := range list.Items {
		if l.Spec.HolderIdentity != nil && *l.Spec.HolderIdentity != "" {
			leases = append(leases, l)
		}
	}
	if len(leases) == 0 {
		return nil, fmt.Errorf("no held leases in %s", set.Namespace)
	}
	return leases, nil
}
