// Licensed to the Apache Software Foundation (ASF) under one
// or more contributor license agreements.  See the NOTICE file
// distributed with this work for additional information
// regarding copyright ownership.  The ASF licenses this file
// to you under the Apache License, Version 2.0 (the
// "License"); you may not use this file except in compliance
// with the License.  You may obtain a copy of the License at
//
//   http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing,
// software distributed under the License is distributed on an
// "AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF ANY
// KIND, either express or implied.  See the License for the
// specific language governing permissions and limitations
// under the License.

package controllers

import (
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	resource "k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	utilpointer "k8s.io/utils/pointer"

	"bytes"
	"context"
	"io"
	"net/url"
	"strconv"
	"strings"
	"time"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	restclient "k8s.io/client-go/rest"
	"k8s.io/client-go/tools/remotecommand"

	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kuduoperatorv1 "github.com/apache/kudu/tree/master/kubernetes/kudu-operator/api/v1"
)

// KuduClusterReconciler reconciles a KuduCluster object
type KuduClusterReconciler struct {
	client.Client
	Log    logr.Logger
	Config *restclient.Config
	Scheme *runtime.Scheme
}

// SetupWithManager sets up the controller with the Manager.
func (r *KuduClusterReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&kuduoperatorv1.KuduCluster{}).
		Owns(&corev1.Service{}).
		Owns(&appsv1.StatefulSet{}).
		Complete(r)
}

//+kubebuilder:rbac:groups=kuduoperator.cluster,resources=kuduclusters,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=kuduoperator.cluster,resources=kuduclusters/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=kuduoperator.cluster,resources=kuduclusters/finalizers,verbs=update
//+kubebuilder:rbac:groups=core,resources=pods,verbs=get
//+kubebuilder:rbac:groups=core,resources=pods/exec,verbs=create
//+kubebuilder:rbac:groups=core,resources=services,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=apps,resources=statefulsets,verbs=get;list;watch;create;update;patch;delete
// Reconcile is triggered by the KuduCluster controller in response to cluster and external events (create/update/delete).
// to bring a KuduCluster resource closer to the desired state defined in the spec.
// For a Kuducluster, Reconcile will create services and statefulsets for the Kudu masters and tservers if they do not exist.
// Reconcile can be immediately requeued to execute again in the event of error or after a period of time
// to wait for resources to be created first.
func (r *KuduClusterReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := r.Log.WithValues("kuducluster", req.NamespacedName)

	// Fetch the KuduCluster instance.
	kuducluster := &kuduoperatorv1.KuduCluster{}
	err := r.Get(ctx, req.NamespacedName, kuducluster)
	if err != nil {
		if errors.IsNotFound(err) {
			log.Info("KuduCluster resource not found.")
			return ctrl.Result{}, nil
		}
		log.Error(err, "Failed to get KuduCluster resource. Will requeue the request.")
		return ctrl.Result{}, err
	}

	// Check if the services and statefulset for the masters already exist. If not, create them.
	requeue_wait_time, masters_err := r.reconcileMasters(log, ctx, kuducluster)
	if masters_err != nil {
		return ctrl.Result{}, masters_err
	}
	if requeue_wait_time != 0 {
		log.Info("After reconciling masters, requeuing KuduCluster::Reconcile() after waiting " + requeue_wait_time.String())
		return ctrl.Result{RequeueAfter: (requeue_wait_time)}, nil
	}

	// Check if the service and statefulset for the tservers already exist. If not, create them.
	requeue_wait_time, tservers_err := r.reconcileTservers(log, ctx, kuducluster)
	if tservers_err != nil {
		return ctrl.Result{}, tservers_err
	}
	if requeue_wait_time != 0 {
		log.Info("After reconciling tservers, requeuing KuduCluster::Reconcile() after waiting " + requeue_wait_time.String())
		return ctrl.Result{RequeueAfter: (requeue_wait_time)}, nil
	}

	return ctrl.Result{}, nil
}

// Reconciles the state of the Kudu masters.
// If a service or statefulset needs to be created, returns the amount of time the caller should
// wait before requeueing the function and completing reconciliation.
func (r *KuduClusterReconciler) reconcileMasters(log logr.Logger, ctx context.Context, kuducluster *kuduoperatorv1.KuduCluster) (time.Duration, error) {

	// Check if the headless service for the masters already exists. If not, create a new one.
	m_service := &corev1.Service{}
	err := r.Get(ctx, types.NamespacedName{Name: getKuduMastersServiceName(), Namespace: kuducluster.Namespace}, m_service)
	if err != nil && errors.IsNotFound(err) {
		// Define a new service.
		mserv := r.serviceForKuduMasters(kuducluster)
		log.Info("Creating a new Service", "Service.Namespace", mserv.Namespace, "Service.Name", mserv.Name)
		err = r.Create(ctx, mserv)
		if err != nil {
			log.Error(err, "Failed to create a new Service", "Service.Namespace", mserv.Namespace, "Service.Name", mserv.Name)
			return 0, err
		}
		// Service created successfully; return and requeue.
		return 0, nil
	} else if err != nil {
		log.Error(err, "Failed to get the Kudu masters service")
		return 0, err
	}

	// Check if the headless service for the master UI already exists. If not, create a new one.
	ui_service := &corev1.Service{}
	err = r.Get(ctx, types.NamespacedName{Name: getKuduMasterUIServiceName(), Namespace: kuducluster.Namespace}, ui_service)
	if err != nil && errors.IsNotFound(err) {
		// Define a new service.
		ui_serv := r.serviceForKuduMasterUI(kuducluster)
		log.Info("Creating a new Service", "Service.Namespace", ui_serv.Namespace, "Service.Name", ui_serv.Name)
		err = r.Create(ctx, ui_serv)
		if err != nil {
			log.Error(err, "Failed to create a new Service", "Service.Namespace", ui_serv.Namespace, "Service.Name", ui_serv.Name)
			return 0, err
		}
		// Service created successfully; return and requeue.
		return 0, nil
	} else if err != nil {
		log.Error(err, "Failed to get the Kudu master UI service")
		return 0, err
	}

	// Check if the statefulset for the masters already exists. If not, create a new one.
	k_masters := &appsv1.StatefulSet{}
	err = r.Get(ctx, types.NamespacedName{Name: "kudu-master", Namespace: kuducluster.Namespace}, k_masters)
	if err != nil && errors.IsNotFound(err) {
		// Define a new statefulset.
		stateset := r.statefulsetForKuduMasters(kuducluster)
		log.Info("Creating a new StatefulSet for the Kudu masters", "StatefulSet.Namespace", stateset.Namespace, "StatefulSet.Name", stateset.Name)
		err = r.Create(ctx, stateset)
		if err != nil {
			log.Error(err, "Failed to create a new StatefulSet for the Kudu masters", "StatefulSet.Namespace", stateset.Namespace, "StatefulSet.Name", stateset.Name)
			return 0, err
		}
		// StatefulSet created successfully; return and requeue after 3 minutes.
		return (3 * time.Minute), nil
	} else if err != nil {
		log.Error(err, "Failed to get the StatefulSet for the Kudu masters")
		return 0, err
	}

	// Check if the statefulset for the masters is the correct size. If not, delete the current one and requeue.
	// Recreating all the masters together avoids the issue of master consensus conflict, but results in losing
	// access to existing tables.
	// TODO: Make increasing the number of masters safe by adding a master.
	master_replicas := kuducluster.Spec.NumMasters
	if *k_masters.Spec.Replicas != master_replicas {
		err = r.Delete(ctx, k_masters)
		if err != nil {
			log.Error(err, "Failed to delete StatefulSet for the Kudu masters")
			return 0, err
		}
		// StatefulSet deleted successfully; return and requeue.
		log.Info("Deleting the StatefulSet for the Kudu masters because it does not have the right number of replicas.")
		return 0, nil
	}

	return 0, nil
}

// Reconciles the state of the Kudu tservers.
// If a service or statefulset needs to be created, returns the amount of time the caller should
// wait before requeueing the function and completing reconciliation.
func (r *KuduClusterReconciler) reconcileTservers(log logr.Logger, ctx context.Context, kuducluster *kuduoperatorv1.KuduCluster) (time.Duration, error) {
	// Check if the headless service for the tservers already exists. If not, create a new one.
	t_service := &corev1.Service{}
	err := r.Get(ctx, types.NamespacedName{Name: "kudu-tservers", Namespace: kuducluster.Namespace}, t_service)
	if err != nil && errors.IsNotFound(err) {
		// Define a new service.
		tserv := r.serviceForKuduTservers(kuducluster)
		log.Info("Creating a new Service", "Service.Namespace", tserv.Namespace, "Service.Name", tserv.Name)
		err = r.Create(ctx, tserv)
		if err != nil {
			log.Error(err, "Failed to create a new Service", "Service.Namespace", tserv.Namespace, "Service.Name", tserv.Name)
			return 0, err
		}
		// Service created successfully; return and requeue.
		return 0, nil
	} else if err != nil {
		log.Error(err, "Failed to get kudu-tservers service")
		return 0, err
	}

	// Check if the statefulset for the tservers already exists. If not, create a new one.
	k_tservers := &appsv1.StatefulSet{}
	err = r.Get(ctx, types.NamespacedName{Name: "kudu-tserver", Namespace: kuducluster.Namespace}, k_tservers)
	if err != nil && errors.IsNotFound(err) {
		// Define a new statefulset.
		stateset := r.statefulsetForKuduTservers(kuducluster)
		log.Info("Creating a new StatefulSet for the Kudu tservers", "StatefulSet.Namespace", stateset.Namespace, "StatefulSet.Name", stateset.Name)
		err = r.Create(ctx, stateset)
		if err != nil {
			log.Error(err, "Failed to create a new StatefulSet for the Kudu tservers", "StatefulSet.Namespace", stateset.Namespace, "StatefulSet.Name", stateset.Name)
			return 0, err
		}
		// StatefulSet created successfully; return and requeue after 3 minutes.
		return (3 * time.Minute), nil
	} else if err != nil {
		log.Error(err, "Failed to get the StatefulSet for the Kudu tservers")
		return 0, err
	}

	// Check if the statefulset for the tservers is the correct size. If not, update the current one.
	// Since the number of tservers has changed, launch the rebalancer, and requeue.
	tserver_replicas := kuducluster.Spec.NumTservers
	if *k_tservers.Spec.Replicas != tserver_replicas {
		k_tservers.Spec.Replicas = &tserver_replicas
		err = r.Update(ctx, k_tservers)
		if err != nil {
			log.Error(err, "Failed to update StatefulSet for the Kudu tservers")
			return 0, err
		}
		// StatefulSet updated successfully; return and requeue.
		log.Info("Updating the StatefulSet for the Kudu tservers because it does not have the right number of replicas.")
		// Launch the rebalancer.
		stdout, stderr, rebalancer_err := r.execRebalance(kuducluster)
		if rebalancer_err != nil {
			log.Error(rebalancer_err, "Failed to rebalance:"+stderr+stdout)
			return 0, rebalancer_err
		} else {
			log.Info("Executing the rebalancer was successful")
		}
		return 0, nil
	}

	return 0, nil
}

// Returns a headless service for the Kudu masters StatefulSet.
func (r *KuduClusterReconciler) serviceForKuduMasters(kuducluster *kuduoperatorv1.KuduCluster) *corev1.Service {
	var serviceNamespace = kuducluster.ObjectMeta.Namespace
	var serviceName = getKuduMastersServiceName()
	var serviceLabels = getMasterLabels()

	var rpcPort = corev1.ServicePort{Name: "rpc", Port: 7051}
	var uiPort = corev1.ServicePort{Name: "ui", Port: 8051}

	var serv = &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: serviceNamespace,
			Name:      serviceName,
			Labels:    serviceLabels,
		},
		Spec: corev1.ServiceSpec{
			ClusterIP: "None",
			Selector:  serviceLabels,
			Ports:     []corev1.ServicePort{rpcPort, uiPort},
		},
	}
	ctrl.SetControllerReference(kuducluster, serv, r.Scheme)
	return serv
}

// Returns a service for the Kudu masters UI.
func (r *KuduClusterReconciler) serviceForKuduMasterUI(kuducluster *kuduoperatorv1.KuduCluster) *corev1.Service {
	var serviceNamespace = kuducluster.ObjectMeta.Namespace
	var serviceName = getKuduMasterUIServiceName()
	var serviceLabels = getMasterLabels()

	var uiPort = corev1.ServicePort{Name: "ui", Port: 8051}

	var serv = &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: serviceNamespace,
			Name:      serviceName,
			Labels:    serviceLabels,
		},
		Spec: corev1.ServiceSpec{
			Type:                  corev1.ServiceTypeNodePort,
			Selector:              serviceLabels,
			Ports:                 []corev1.ServicePort{uiPort},
			ExternalTrafficPolicy: "Local",
		},
	}
	ctrl.SetControllerReference(kuducluster, serv, r.Scheme)
	return serv
}

// Returns a headless service for the Kudu tservers StatefulSet.
func (r *KuduClusterReconciler) serviceForKuduTservers(kuducluster *kuduoperatorv1.KuduCluster) *corev1.Service {
	var serviceNamespace = kuducluster.ObjectMeta.Namespace
	var serviceName = getKuduTserversServiceName()
	var serviceLabels = getTserverLabels()

	var rpcPort = corev1.ServicePort{Name: "rpc", Port: 7050}
	var uiPort = corev1.ServicePort{Name: "ui", Port: 8050}

	var serv = &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: serviceNamespace,
			Name:      serviceName,
			Labels:    serviceLabels,
		},
		Spec: corev1.ServiceSpec{
			ClusterIP: "None",
			Selector:  serviceLabels,
			Ports:     []corev1.ServicePort{rpcPort, uiPort},
		},
	}
	ctrl.SetControllerReference(kuducluster, serv, r.Scheme)
	return serv
}

// Returns a StatefulSet for the Kudu masters.
func (r *KuduClusterReconciler) statefulsetForKuduMasters(kuducluster *kuduoperatorv1.KuduCluster) *appsv1.StatefulSet {
	var kuduMasterNamespace = kuducluster.ObjectMeta.Namespace
	var kuduMasterName = getKuduMasterName()
	var kuduMastersServiceName = getKuduMastersServiceName()
	var ls = getMasterLabels()
	var replicas = kuducluster.Spec.NumMasters
	var masterAddresses = getMasterAddresses(kuducluster)

	var envVars = getPodEnv(masterAddresses)

	var containerPorts = []corev1.ContainerPort{
		{
			ContainerPort: 8051,
			Name:          "master-ui",
		},
		{
			ContainerPort: 7051,
			Name:          "master-rpc",
		},
	}

	var volumeMounts = []corev1.VolumeMount{
		{
			Name:      "datadir",
			MountPath: "/mnt/data0",
		},
	}

	var podAntiAffinity = getPodAntiAffinity([]string{kuduMasterName})

	var podSpec = corev1.PodSpec{
		Containers: []corev1.Container{{
			Name:            "kudu-master",
			Image:           "apache/kudu",
			ImagePullPolicy: corev1.PullIfNotPresent,
			Env:             envVars,
			Args:            []string{"master"},
			Ports:           containerPorts,
			VolumeMounts:    volumeMounts,
		}},
		Affinity: &corev1.Affinity{
			PodAntiAffinity: podAntiAffinity,
		},
	}

	var volumeClaimTemplates = getVolumeClaimTemplates()

	var statefulset = &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: kuduMasterNamespace,
			Name:      kuduMasterName,
			Labels:    ls,
		},
		Spec: appsv1.StatefulSetSpec{
			ServiceName:         kuduMastersServiceName,
			PodManagementPolicy: "Parallel",
			Replicas:            &replicas,
			Selector:            &metav1.LabelSelector{MatchLabels: ls},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: ls,
				},
				Spec: podSpec,
			},
			VolumeClaimTemplates: volumeClaimTemplates,
			UpdateStrategy: appsv1.StatefulSetUpdateStrategy{
				Type: appsv1.RollingUpdateStatefulSetStrategyType,
			},
		},
	}

	ctrl.SetControllerReference(kuducluster, statefulset, r.Scheme)
	return statefulset
}

// Returns a StatefulSet for the Kudu tservers.
func (r *KuduClusterReconciler) statefulsetForKuduTservers(kuducluster *kuduoperatorv1.KuduCluster) *appsv1.StatefulSet {
	var kuduTserverNamespace = kuducluster.ObjectMeta.Namespace
	var kuduTserverName = getKuduTserverName()
	var kuduTserversServiceName = getKuduTserversServiceName()
	var ls = getTserverLabels()
	var replicas = kuducluster.Spec.NumTservers
	var masterAddresses = getMasterAddresses(kuducluster)

	var envVars = getPodEnv(masterAddresses)

	var containerPorts = []corev1.ContainerPort{
		{
			ContainerPort: 8050,
			Name:          "tserver-ui",
		},
		{
			ContainerPort: 7050,
			Name:          "tserver-rpc",
		},
	}

	var volumeMounts = []corev1.VolumeMount{
		{
			Name:      "datadir",
			MountPath: "/mnt/data0",
		},
	}

	var podAntiAffinity = getPodAntiAffinity([]string{kuduTserverName})

	var podSpec = corev1.PodSpec{
		Containers: []corev1.Container{{
			Name:            "kudu-tserver",
			Image:           "apache/kudu",
			ImagePullPolicy: corev1.PullIfNotPresent,
			Env:             envVars,
			Args:            []string{"tserver"},
			Ports:           containerPorts,
			VolumeMounts:    volumeMounts,
		}},
		Affinity: &corev1.Affinity{
			PodAntiAffinity: podAntiAffinity,
		},
	}

	var volumeClaimTemplates = getVolumeClaimTemplates()

	var statefulset = &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: kuduTserverNamespace,
			Name:      kuduTserverName,
			Labels:    ls,
		},
		Spec: appsv1.StatefulSetSpec{
			ServiceName:         kuduTserversServiceName,
			PodManagementPolicy: "Parallel",
			Replicas:            &replicas,
			Selector:            &metav1.LabelSelector{MatchLabels: ls},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: ls,
				},
				Spec: podSpec,
			},
			VolumeClaimTemplates: volumeClaimTemplates,
			UpdateStrategy: appsv1.StatefulSetUpdateStrategy{
				Type: appsv1.RollingUpdateStatefulSetStrategyType,
				RollingUpdate: &appsv1.RollingUpdateStatefulSetStrategy{
					Partition: utilpointer.Int32Ptr(0),
				},
			},
		},
	}

	ctrl.SetControllerReference(kuducluster, statefulset, r.Scheme)
	return statefulset
}

// Launches the Kudu rebalancer.
func (r *KuduClusterReconciler) execRebalance(kuducluster *kuduoperatorv1.KuduCluster) (string, string, error) {
	kuduMasterPod := "kudu-master-0"
	kuduMasterAddreses := getMasterAddresses(kuducluster)
	rebalance_cmd := "kudu cluster rebalance " + kuduMasterAddreses
	cmd := []string{"bash", "-c", rebalance_cmd}

	return kubectlExec(r.Config, kuducluster.Namespace, kuduMasterPod, "", cmd, nil)
}

func getKuduMasterName() string           { return "kudu-master" }
func getKuduTserverName() string          { return "kudu-tserver" }
func getKuduMastersServiceName() string   { return "kudu-masters" }
func getKuduMasterUIServiceName() string  { return "kudu-master-ui" }
func getKuduTserversServiceName() string  { return "kudu-tservers" }
func getMasterLabels() map[string]string  { return map[string]string{"app": "kudu-master"} }
func getTserverLabels() map[string]string { return map[string]string{"app": "kudu-tserver"} }

// Returns the addresses of the Kudu masters.
func getMasterAddresses(kuducluster *kuduoperatorv1.KuduCluster) string {
	var replicas = int(kuducluster.Spec.NumMasters)
	var namespace = kuducluster.ObjectMeta.Namespace
	var addresses = ""

	for i := 0; i < replicas; i++ {
		if i != 0 {
			addresses = addresses + ","
		}
		addresses = addresses + "kudu-master-" + strconv.Itoa(i) + ".kudu-masters." + namespace + ".svc.cluster.local"
	}

	return addresses
}

// Returns the environment for the pods in the cluster.
func getPodEnv(masterAddresses string) []corev1.EnvVar {
	var envVars = []corev1.EnvVar{
		{
			Name:  "GET_HOSTS_FROM",
			Value: "dns",
		},
		{
			Name: "POD_IP",
			ValueFrom: &corev1.EnvVarSource{
				FieldRef: &corev1.ObjectFieldSelector{
					FieldPath: "status.podIP",
				},
			},
		},
		{
			Name: "POD_NAME",
			ValueFrom: &corev1.EnvVarSource{
				FieldRef: &corev1.ObjectFieldSelector{
					FieldPath: "metadata.name",
				},
			},
		},
		{
			Name:  "KUDU_MASTERS",
			Value: masterAddresses,
		},
	}
	return envVars
}

// Returns the AntiAffinity for the pods in the cluster.
func getPodAntiAffinity(values []string) *corev1.PodAntiAffinity {
	var podAntiAffinity = &corev1.PodAntiAffinity{
		PreferredDuringSchedulingIgnoredDuringExecution: []corev1.WeightedPodAffinityTerm{
			{
				Weight: 100,
				PodAffinityTerm: corev1.PodAffinityTerm{
					LabelSelector: &metav1.LabelSelector{
						MatchExpressions: []metav1.LabelSelectorRequirement{
							{
								Key:      "app",
								Operator: metav1.LabelSelectorOpIn,
								Values:   values,
							},
						},
					},
					TopologyKey: "kubernetes.io/hostname",
				},
			},
		},
	}
	return podAntiAffinity
}

// Returns the VolumeClaimTemplate for the StatefulSets.
func getVolumeClaimTemplates() []corev1.PersistentVolumeClaim {
	standard := "standard"

	var persistentVolumeClaimSpec = corev1.PersistentVolumeClaimSpec{
		AccessModes: []corev1.PersistentVolumeAccessMode{
			"ReadWriteOnce",
		},
		Resources: corev1.ResourceRequirements{
			Requests: corev1.ResourceList{
				corev1.ResourceStorage: resource.MustParse("10Gi"),
			},
		},
		StorageClassName: &standard,
	}

	var volumeClaimTemplates = []corev1.PersistentVolumeClaim{
		{
			ObjectMeta: metav1.ObjectMeta{
				Name: "datadir",
			},
			Spec: persistentVolumeClaimSpec,
		},
	}
	return volumeClaimTemplates
}

// Launches kubectl exec with a specified command.
func kubectlExec(config *restclient.Config, namespace string, pod string, container string,
	command []string, stdin io.Reader) (string, string, error) {
	const tty = false
	kubeCli, err := kubernetes.NewForConfig(config)
	if err != nil {
		return "", "", err
	}

	req := kubeCli.CoreV1().RESTClient().Post().
		Resource("pods").
		Name(pod).
		Namespace(namespace).
		SubResource("exec")

	// Add container name if passed
	if len(container) != 0 {
		req.Param("container", container)
	}
	for _, c := range command {
		req.Param("command", c)
	}
	req.VersionedParams(&corev1.PodExecOptions{
		Container: container,
		Command:   command,
		Stdin:     stdin != nil,
		Stdout:    true,
		Stderr:    true,
		TTY:       tty,
	}, scheme.ParameterCodec)

	var stdout, stderr bytes.Buffer
	err = execute("POST", req.URL(), config, stdin, &stdout, &stderr, tty)
	return strings.TrimSpace(stdout.String()), strings.TrimSpace(stderr.String()), err
}

// Helper function for kubectlExec().
func execute(method string, url *url.URL, config *restclient.Config, stdin io.Reader, stdout, stderr io.Writer, tty bool) error {
	exec, err := remotecommand.NewSPDYExecutor(config, method, url)
	if err != nil {
		return err
	}
	return exec.Stream(remotecommand.StreamOptions{
		Stdin:  stdin,
		Stdout: stdout,
		Stderr: stderr,
		Tty:    tty,
	})
}
