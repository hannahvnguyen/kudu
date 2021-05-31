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

	kuduv1 "github.com/apache/kudu/tree/master/kubernetes/kudu-operator/api/v1"
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
		For(&kuduv1.KuduCluster{}).
		Owns(&corev1.Service{}).
		Owns(&appsv1.StatefulSet{}).
		Complete(r)
}

//+kubebuilder:rbac:groups=kuduoperator.capstone,resources=kuduclusters,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=kuduoperator.capstone,resources=kuduclusters/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=kuduoperator.capstone,resources=kuduclusters/finalizers,verbs=update
//+kubebuilder:rbac:groups=core,resources=pods,verbs=get
//+kubebuilder:rbac:groups=core,resources=pods/exec,verbs=create
//+kubebuilder:rbac:groups=core,resources=services,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=apps,resources=statefulsets,verbs=get;list;watch;create;update;patch;delete
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
		return ctrl.Result{}, nil
	}
	if requeue_wait_time != 0 {
		log.Info("After reconciling tservers, requeuing KuduCluster::Reconcile() after waiting " + requeue_wait_time.String())
		return ctrl.Result{RequeueAfter: (requeue_wait_time)}, nil
	}

	// Ksck the cluster.
	// stdout, stderr, ksck_err := r.execKsck(kuducluster)
	// if ksck_err != nil {
	// 	log.Error(ksck_err, "Failed to ksck:"+stderr+stdout)
	// 	return ctrl.Result{}, ksck_err
	// } else {
	// 	log.Info("Executing ksck was successful")
	// }

	return ctrl.Result{}, nil
}

func (r *KuduClusterReconciler) reconcileMasters(log logr.Logger, ctx context.Context, kuducluster *kuduv1.KuduCluster) (time.Duration, error) {

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
	kmasters := &appsv1.StatefulSet{}
	err = r.Get(ctx, types.NamespacedName{Name: "kudu-master", Namespace: kuducluster.Namespace}, kmasters)
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

	return 0, nil
}

func (r *KuduClusterReconciler) reconcileTservers(log logr.Logger, ctx context.Context, kuducluster *kuduv1.KuduCluster) (time.Duration, error) {
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
		log.Error(err, "Failed to get kudu-masters service")
		return 0, err
	}

	// Check if the statefulset for the tservers already exists. If not, create a new one.
	ktservers := &appsv1.StatefulSet{}
	err = r.Get(ctx, types.NamespacedName{Name: "kudu-tserver", Namespace: kuducluster.Namespace}, ktservers)
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
		log.Error(err, "Failed to get the StatefulSet for the kudu tservers")
		return 0, err
	}

	return 0, nil
}

func (r *KuduClusterReconciler) serviceForKuduMasters(m *kuduv1.KuduCluster) *corev1.Service {
	var serviceNamespace = m.ObjectMeta.Namespace
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
	ctrl.SetControllerReference(m, serv, r.Scheme)
	return serv
}

func (r *KuduClusterReconciler) serviceForKuduMasterUI(m *kuduv1.KuduCluster) *corev1.Service {
	var serviceNamespace = m.ObjectMeta.Namespace
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
	ctrl.SetControllerReference(m, serv, r.Scheme)
	return serv
}

func (r *KuduClusterReconciler) serviceForKuduTservers(m *kuduv1.KuduCluster) *corev1.Service {
	var serviceNamespace = m.ObjectMeta.Namespace
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
	ctrl.SetControllerReference(m, serv, r.Scheme)
	return serv
}

func (r *KuduClusterReconciler) statefulsetForKuduMasters(m *kuduv1.KuduCluster) *appsv1.StatefulSet {
	var kuduMasterNamespace = m.ObjectMeta.Namespace
	var kuduMasterName = getKuduMasterName()
	var kuduMastersServiceName = getKuduMastersServiceName()
	var ls = getMasterLabels()
	var replicas = m.Spec.NumMasters
	var addresses = namesForKuduMasters(m)

	var envVars = getPodEnv(addresses)

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

	var stateset = &appsv1.StatefulSet{
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

	ctrl.SetControllerReference(m, stateset, r.Scheme)
	return stateset
}

func (r *KuduClusterReconciler) statefulsetForKuduTservers(m *kuduv1.KuduCluster) *appsv1.StatefulSet {
	var kuduTserverNamespace = m.ObjectMeta.Namespace
	var kuduTserverName = getKuduTserverName()
	var kuduTserversServiceName = getKuduTserversServiceName()
	var ls = getTserverLabels()
	var replicas = m.Spec.NumTservers
	var addresses = namesForKuduMasters(m)

	var envVars = getPodEnv(addresses)

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

	var stateset = &appsv1.StatefulSet{
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

	ctrl.SetControllerReference(m, stateset, r.Scheme)
	return stateset
}

func (r *KuduClusterReconciler) execKsck(kuducluster *kuduv1.KuduCluster) (string, string, error) {
	kuduMasterPod := "kudu-master-0"
	kuduMasterNames := namesForKuduMasters(kuducluster)
	ksck_cmd := "kudu cluster ksck " + kuduMasterNames
	cmd := []string{"bash", "-c", ksck_cmd}

	return kubectlExec(r.Config, kuducluster.Namespace, kuduMasterPod, "", cmd, nil)
}

func (r *KuduClusterReconciler) execRebalance(kuducluster *kuduv1.KuduCluster) (string, string, error) {
	kuduMasterPod := "kudu-master-0"
	kuduMasterNames := namesForKuduMasters(kuducluster)
	rebalance_cmd := "kudu cluster rebalance " + kuduMasterNames
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

func namesForKuduMasters(m *kuduv1.KuduCluster) string {
	var replicas = int(m.Spec.NumMasters)
	var namespace = m.ObjectMeta.Namespace
	var addresses = ""

	for i := 0; i < replicas; i++ {
		if i != 0 {
			addresses = addresses + ","
		}
		addresses = addresses + "kudu-master-" + strconv.Itoa(i) + ".kudu-masters." + namespace + ".svc.cluster.local"
	}

	return addresses
}

func getPodEnv(addresses string) []corev1.EnvVar {
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
			Value: addresses,
		},
	}
	return envVars
}

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
