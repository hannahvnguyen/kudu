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
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"context"

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
//+kubebuilder:rbac:groups=core,resources=services,verbs=get;list;watch;create;update;patch;delete

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
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

	// Check if the headless service for the masters already exists. If not, create a new one.
	m_service := &corev1.Service{}
	err = r.Get(ctx, types.NamespacedName{Name: "kudu-masters", Namespace: kuducluster.Namespace}, m_service)
	if err != nil && errors.IsNotFound(err) {
		// Define a new service.
		mserv := r.serviceForKuduMasters(kuducluster)
		log.Info("Creating a new service", "Service.Namespace", mserv.Namespace, "Service.Name", mserv.Name)
		err = r.Create(ctx, mserv)
		if err != nil {
			log.Error(err, "Failed to create a new Service", "Service.Namespace", mserv.Namespace, "Service.Name", mserv.Name)
			return ctrl.Result{}, err
		}
		// Service created successfully; return and requeue.
		return ctrl.Result{Requeue: true}, nil
	} else if err != nil {
		log.Error(err, "Failed to get kudu-masters service")
		return ctrl.Result{}, err
	}

	// Check if the headless service for the master UI already exists. If not, create a new one.
	ui_service := &corev1.Service{}
	err = r.Get(ctx, types.NamespacedName{Name: "kudu-master-ui", Namespace: kuducluster.Namespace}, ui_service)
	if err != nil && errors.IsNotFound(err) {
		// Define a new service.
		ui_serv := r.serviceForKuduMasterUI(kuducluster)
		log.Info("Creating a new service", "Service.Namespace", ui_serv.Namespace, "Service.Name", ui_serv.Name)
		err = r.Create(ctx, ui_serv)
		if err != nil {
			log.Error(err, "Failed to create a new Service", "Service.Namespace", ui_serv.Namespace, "Service.Name", ui_serv.Name)
			return ctrl.Result{}, err
		}
		// Service created successfully; return and requeue.
		return ctrl.Result{Requeue: true}, nil
	} else if err != nil {
		log.Error(err, "Failed to get kudu-master-ui service")
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

func (r *KuduClusterReconciler) serviceForKuduMasters(m *kuduv1.KuduCluster) *corev1.Service {
	var kuduMastersNamespace = m.ObjectMeta.Namespace
	var kuduMastersServiceName = "kudu-masters"
	var serviceLabels = getMasterLabels()

	var rpcPort = corev1.ServicePort{Name: "rpc", Port: 7051}
	var uiPort = corev1.ServicePort{Name: "ui", Port: 8051}

	var serv = &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: kuduMastersNamespace,
			Name:      kuduMastersServiceName,
			Labels:    serviceLabels,
		},
		Spec: corev1.ServiceSpec{
			ClusterIP: "None",
			Selector:  serviceLabels,
			Ports:     []corev1.ServicePort{rpcPort, uiPort},
		},
	}
	ctrl.SetControllerReference(m, serv, r.Scheme)
	return serv
}

func (r *KuduClusterReconciler) serviceForKuduMasterUI(m *kuduv1.KuduCluster) *corev1.Service {
	var kuduMasterUINamespace = m.ObjectMeta.Namespace
	var kuduMasterUIServiceName = "kudu-master-ui"
	var serviceLabels = getMasterLabels()

	var uiPort = corev1.ServicePort{Name: "ui", Port: 8051}

	var serv = &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: kuduMasterUINamespace,
			Name:      kuduMasterUIServiceName,
			Labels:    serviceLabels,
		},
		Spec: corev1.ServiceSpec{
			Type:                  corev1.ServiceTypeNodePort,
			Selector:              serviceLabels,
			Ports:                 []corev1.ServicePort{uiPort},
			ExternalTrafficPolicy: "Local",
		},
	}
	ctrl.SetControllerReference(m, serv, r.Scheme)
	return serv
}

func getMasterLabels() map[string]string {
	return map[string]string{"app": "kudu-master"}
}

// SetupWithManager sets up the controller with the Manager.
func (r *KuduClusterReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&kuduv1.KuduCluster{}).
		Owns(&corev1.Service{}).
		Owns(&corev1.Service{}).
		Complete(r)
}
