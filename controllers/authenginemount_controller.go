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

package controllers

import (
	"context"
	"errors"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/go-logr/logr"
	vault "github.com/hashicorp/vault/api"
	"github.com/redhat-cop/operator-utils/pkg/util"
	redhatcopv1alpha1 "github.com/redhat-cop/vault-config-operator/api/v1alpha1"
	vaultutils "github.com/redhat-cop/vault-config-operator/api/v1alpha1/utils"
)

// AuthEngineMountReconciler reconciles a AuthEngineMount object
type AuthEngineMountReconciler struct {
	util.ReconcilerBase
	Log            logr.Logger
	ControllerName string
}

//+kubebuilder:rbac:groups=redhatcop.redhat.io,resources=authenginemounts,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=redhatcop.redhat.io,resources=authenginemounts/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=redhatcop.redhat.io,resources=authenginemounts/finalizers,verbs=update

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the AuthEngineMount object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.9.2/pkg/reconcile
func (r *AuthEngineMountReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	_ = log.FromContext(ctx)

	// your logic here

	// Fetch the instance
	instance := &redhatcopv1alpha1.AuthEngineMount{}
	err := r.GetClient().Get(ctx, req.NamespacedName, instance)
	if err != nil {
		if apierrors.IsNotFound(err) {
			// Request object not found, could have been deleted after reconcile request.
			// Owned objects are automatically garbage collected. For additional cleanup logic use finalizers.
			// Return and don't requeue
			return reconcile.Result{}, nil
		}
		// Error reading the object - requeue the request.
		return reconcile.Result{}, err
	}

	if ok, err := r.IsValid(instance); !ok {
		return r.ManageError(ctx, instance, err)
	}

	if ok := r.IsInitialized(instance); !ok {
		err := r.GetClient().Update(ctx, instance)
		if err != nil {
			r.Log.Error(err, "unable to update instance", "instance", instance)
			return r.ManageError(ctx, instance, err)
		}
		return reconcile.Result{}, nil
	}

	if util.IsBeingDeleted(instance) {
		if !util.HasFinalizer(instance, r.ControllerName) {
			return reconcile.Result{}, nil
		}
		err := r.manageCleanUpLogic(ctx, instance)
		if err != nil {
			r.Log.Error(err, "unable to delete instance", "instance", instance)
			return r.ManageError(ctx, instance, err)
		}
		util.RemoveFinalizer(instance, r.ControllerName)
		err = r.GetClient().Update(ctx, instance)
		if err != nil {
			r.Log.Error(err, "unable to update instance", "instance", instance)
			return r.ManageError(ctx, instance, err)
		}
		return reconcile.Result{}, nil
	}

	err = r.manageReconcileLogic(ctx, instance)
	if err != nil {
		r.Log.Error(err, "unable to complete reconcile logic", "instance", instance)
		return r.ManageError(ctx, instance, err)
	}

	return r.ManageSuccess(ctx, instance)
}

// SetupWithManager sets up the controller with the Manager.
func (r *AuthEngineMountReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&redhatcopv1alpha1.SecretEngineMount{}, builder.WithPredicates(util.ResourceGenerationOrFinalizerChangedPredicate{})).
		Complete(r)
}

func (r *AuthEngineMountReconciler) IsValid(obj metav1.Object) (bool, error) {
	return true, nil
}

func (r *AuthEngineMountReconciler) IsInitialized(obj metav1.Object) bool {
	isInitialized := true
	cobj, ok := obj.(client.Object)
	if !ok {
		r.Log.Error(errors.New("unable to convert to client.Object"), "unable to convert to client.Object")
		return false
	}
	if !util.HasFinalizer(cobj, r.ControllerName) {
		util.AddFinalizer(cobj, r.ControllerName)
		isInitialized = false
	}
	instance, ok := obj.(*redhatcopv1alpha1.SecretEngineMount)
	if !ok {
		r.Log.Error(errors.New("unable to convert to redhatcopv1alpha1.SecretEngineMount"), "unable to convert to redhatcopv1alpha1.SecretEngineMount")
		return false
	}
	if instance.Spec.Authentication.ServiceAccount == nil {
		instance.Spec.Authentication.ServiceAccount = &corev1.LocalObjectReference{
			Name: "default",
		}
		isInitialized = false
	}
	return isInitialized
}

func (r *AuthEngineMountReconciler) manageCleanUpLogic(context context.Context, instance *redhatcopv1alpha1.AuthEngineMount) error {
	vaultEndpoint, err := vaultutils.NewVaultEndpoint(context, &instance.Spec.Authentication, nil, instance.Namespace, r.GetClient(), r.Log.WithName("vaultutils"))
	if err != nil {
		r.Log.Error(err, "unable to initialize vaultEndpoint")
		return err
	}
	_, err = vaultEndpoint.GetVaultClient().Sys().MountConfig(instance.GetPath())
	if err != nil {
		if respErr, ok := err.(*vault.ResponseError); ok {
			if respErr.StatusCode == 404 {
				return nil
			}
		}
		r.Log.Error(err, "unable to retrieve", "secretEngineMount", instance.Name)
		return err
	}
	err = vaultEndpoint.GetVaultClient().Sys().Unmount(instance.GetPath())
	if err != nil {
		r.Log.Error(err, "unable to delete", "secretEngineMount", instance.Name)
		return err
	}
	return nil
}

func (r *AuthEngineMountReconciler) manageReconcileLogic(context context.Context, instance *redhatcopv1alpha1.AuthEngineMount) error {
	vaultEndpoint, err := vaultutils.NewVaultEndpoint(context, &instance.Spec.Authentication, nil, instance.Namespace, r.GetClient(), r.Log.WithName("vaultutils"))
	if err != nil {
		r.Log.Error(err, "unable to initialize vaultEndpoint with", "instance", instance)
		return err
	}
	secretEngineMount, err := vaultEndpoint.GetVaultClient().Sys().MountConfig(instance.GetPath())
	if err != nil {
		if respErr, ok := err.(*vault.ResponseError); ok {
			if respErr.StatusCode == 404 || respErr.StatusCode == 400 {
				err = vaultEndpoint.GetVaultClient().Sys().Mount(instance.GetPath(), instance.Spec.GetMountInputFromMount())
				if err != nil {
					r.Log.Error(err, "unable to create", "secretEngineMount", instance.Name)
					return err
				}
			}
		}
		r.Log.Error(err, "unable to retrieve", "secretEngineMount", instance.Name)
		return err
	}
	r.Log.V(1).Info("comparing", "current state", secretEngineMount, "desired state", instance.Spec.AuthMount.Config)
	if !instance.Spec.AuthMount.Config.IsEquivalentTo(secretEngineMount) {
		err = vaultEndpoint.GetVaultClient().Sys().TuneMount(instance.GetPath(), instance.Spec.GetMountInputFromMount().Config)
		if err != nil {
			r.Log.Error(err, "unable to update", "secretEngineMount", instance.Name)
			return err
		}
	}
	return nil
}
