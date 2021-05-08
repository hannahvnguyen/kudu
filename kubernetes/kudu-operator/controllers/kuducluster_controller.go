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
	"k8s.io/apimachinery/pkg/api/errors"

	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kuduv1 "github.com/apache/kudu/tree/master/kubernetes/kudu-operator/api/v1"
)

// KuduClusterReconciler reconciles a KuduCluster object
type KuduClusterReconciler struct {
	client.Client
	Log    logr.Logger
	Scheme *runtime.Scheme
}

//+kubebuilder:rbac:groups=kuduoperator.capstone,resources=kuduclusters,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=kuduoperator.capstone,resources=kuduclusters/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=kuduoperator.capstone,resources=kuduclusters/finalizers,verbs=update

func (r *KuduClusterReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := r.Log.WithValues("kuducluster", req.NamespacedName)

	// Fetch the KuduCluster instance.
	kuducluster := &kuduv1.KuduCluster{}
	err := r.Get(ctx, req.NamespacedName, kuducluster)
	if err != nil {
		if errors.IsNotFound(err) {
			log.Info("KuduCluster resource not found.")
			return ctrl.Result{}, nil
		}
		log.Error(err, "Failed to get KuduCluster resource. Will requeue the request.")
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *KuduClusterReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&kuduv1.KuduCluster{}).
		Complete(r)
}
