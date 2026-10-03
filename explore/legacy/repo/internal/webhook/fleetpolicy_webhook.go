// Package webhook validates FleetPolicies.
package webhook

import (
	"context"
	"errors"

	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	fleetv1 "github.com/acme/fleet-addons/api/v1alpha1"
)

// +kubebuilder:webhook:path=/validate-fleet-acme-io-v1alpha1-fleetpolicy,mutating=false,failurePolicy=fail,sideEffects=None,groups=fleet.acme.io,resources=fleetpolicies,verbs=create;update,versions=v1alpha1,name=vfleetpolicy.kb.io,admissionReviewVersions=v1

type Validator struct{}

func Setup(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr).For(&fleetv1.FleetPolicy{}).WithValidator(&Validator{}).Complete()
}

func (v *Validator) ValidateCreate(_ context.Context, obj runtime.Object) (admission.Warnings, error) {
	return nil, validate(obj.(*fleetv1.FleetPolicy))
}

func (v *Validator) ValidateUpdate(_ context.Context, _, obj runtime.Object) (admission.Warnings, error) {
	return nil, validate(obj.(*fleetv1.FleetPolicy))
}

func (v *Validator) ValidateDelete(context.Context, runtime.Object) (admission.Warnings, error) {
	return nil, nil
}

func validate(p *fleetv1.FleetPolicy) error {
	if p.Spec.Message == "" {
		return errors.New("spec.message must not be empty")
	}
	return nil
}
