/*
Copyright 2019 The Kubernetes Authors.

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
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/Azure/go-autorest/autorest"
	"github.com/Azure/go-autorest/autorest/azure/auth"
	"github.com/go-logr/logr"
	"github.com/golang/mock/gomock"
	"github.com/google/go-cmp/cmp"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	utilfeature "k8s.io/component-base/featuregate/testing"
	"k8s.io/utils/pointer"
	infrav1 "sigs.k8s.io/cluster-api-provider-azure/api/v1beta1"
	"sigs.k8s.io/cluster-api-provider-azure/azure/scope"
	"sigs.k8s.io/cluster-api-provider-azure/internal/test/mock_log"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	expv1 "sigs.k8s.io/cluster-api/exp/api/v1beta1"
	capifeature "sigs.k8s.io/cluster-api/feature"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

var (
	cpName      = "my-managed-cp"
	clusterName = "my-cluster"
)

func TestAzureClusterToAzureMachinesMapper(t *testing.T) {
	g := NewWithT(t)
	scheme := setupScheme(g)
	clusterName := "my-cluster"
	initObjects := []runtime.Object{
		newCluster(clusterName),
		// Create two Machines with an infrastructure ref and one without.
		newMachineWithInfrastructureRef(clusterName, "my-machine-0"),
		newMachineWithInfrastructureRef(clusterName, "my-machine-1"),
		newMachine(clusterName, "my-machine-2"),
	}
	client := fake.NewClientBuilder().WithScheme(scheme).WithRuntimeObjects(initObjects...).Build()

	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	sink := mock_log.NewMockLogSink(mockCtrl)
	sink.EXPECT().Init(logr.RuntimeInfo{CallDepth: 1})
	sink.EXPECT().WithValues("AzureCluster", "my-cluster", "Namespace", "default")
	mapper, err := AzureClusterToAzureMachinesMapper(context.Background(), client, &infrav1.AzureMachine{}, scheme, logr.New(sink))
	g.Expect(err).NotTo(HaveOccurred())

	requests := mapper(&infrav1.AzureCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      clusterName,
			Namespace: "default",
			OwnerReferences: []metav1.OwnerReference{
				{
					Name:       clusterName,
					Kind:       "Cluster",
					APIVersion: clusterv1.GroupVersion.String(),
				},
			},
		},
	})
	g.Expect(requests).To(HaveLen(2))
}

func TestGetCloudProviderConfig(t *testing.T) {
	g := NewWithT(t)
	scheme := runtime.NewScheme()
	_ = clusterv1.AddToScheme(scheme)
	_ = infrav1.AddToScheme(scheme)

	cluster := newCluster("foo")
	azureCluster := newAzureCluster("bar")
	azureCluster.Default()
	azureClusterCustomVnet := newAzureClusterWithCustomVnet("bar")
	azureClusterCustomVnet.Default()

	cases := map[string]struct {
		cluster                    *clusterv1.Cluster
		azureCluster               *infrav1.AzureCluster
		identityType               infrav1.VMIdentity
		identityID                 string
		machinePoolFeature         bool
		expectedControlPlaneConfig string
		expectedWorkerNodeConfig   string
	}{
		"serviceprincipal": {
			cluster:                    cluster,
			azureCluster:               azureCluster,
			identityType:               infrav1.VMIdentityNone,
			expectedControlPlaneConfig: spControlPlaneCloudConfig,
			expectedWorkerNodeConfig:   spWorkerNodeCloudConfig,
		},
		"system-assigned-identity": {
			cluster:                    cluster,
			azureCluster:               azureCluster,
			identityType:               infrav1.VMIdentitySystemAssigned,
			expectedControlPlaneConfig: systemAssignedControlPlaneCloudConfig,
			expectedWorkerNodeConfig:   systemAssignedWorkerNodeCloudConfig,
		},
		"user-assigned-identity": {
			cluster:                    cluster,
			azureCluster:               azureCluster,
			identityType:               infrav1.VMIdentityUserAssigned,
			identityID:                 "foobar",
			expectedControlPlaneConfig: userAssignedControlPlaneCloudConfig,
			expectedWorkerNodeConfig:   userAssignedWorkerNodeCloudConfig,
		},
		"serviceprincipal with custom vnet": {
			cluster:                    cluster,
			azureCluster:               azureClusterCustomVnet,
			identityType:               infrav1.VMIdentityNone,
			expectedControlPlaneConfig: spCustomVnetControlPlaneCloudConfig,
			expectedWorkerNodeConfig:   spCustomVnetWorkerNodeCloudConfig,
		},
		"with rate limits": {
			cluster:                    cluster,
			azureCluster:               withRateLimits(*azureCluster),
			identityType:               infrav1.VMIdentityNone,
			expectedControlPlaneConfig: rateLimitsControlPlaneCloudConfig,
			expectedWorkerNodeConfig:   rateLimitsWorkerNodeCloudConfig,
		},
		"with back-off config": {
			cluster:                    cluster,
			azureCluster:               withbackOffConfig(*azureCluster),
			identityType:               infrav1.VMIdentityNone,
			expectedControlPlaneConfig: backOffCloudConfig,
			expectedWorkerNodeConfig:   backOffCloudConfig,
		},
		"with machinepools": {
			cluster:                    cluster,
			azureCluster:               azureCluster,
			identityType:               infrav1.VMIdentityNone,
			machinePoolFeature:         true,
			expectedControlPlaneConfig: vmssCloudConfig,
			expectedWorkerNodeConfig:   vmssCloudConfig,
		},
	}

	os.Setenv(auth.ClientID, "fooClient")
	os.Setenv(auth.ClientSecret, "fooSecret")
	os.Setenv(auth.TenantID, "fooTenant")

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if tc.machinePoolFeature {
				defer utilfeature.SetFeatureGateDuringTest(t, capifeature.Gates, capifeature.MachinePool, true)()
			}
			initObjects := []runtime.Object{tc.cluster, tc.azureCluster}
			fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithRuntimeObjects(initObjects...).Build()

			clusterScope, err := scope.NewClusterScope(context.Background(), scope.ClusterScopeParams{
				AzureClients: scope.AzureClients{
					Authorizer: autorest.NullAuthorizer{},
				},
				Cluster:      tc.cluster,
				AzureCluster: tc.azureCluster,
				Client:       fakeClient,
			})
			g.Expect(err).NotTo(HaveOccurred())

			cloudConfig, err := GetCloudProviderSecret(clusterScope, "default", "foo", metav1.OwnerReference{}, tc.identityType, tc.identityID)
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(cloudConfig.Data).NotTo(BeNil())

			if diff := cmp.Diff(tc.expectedControlPlaneConfig, string(cloudConfig.Data["control-plane-azure.json"])); diff != "" {
				t.Errorf(diff)
			}
			if diff := cmp.Diff(tc.expectedWorkerNodeConfig, string(cloudConfig.Data["worker-node-azure.json"])); diff != "" {
				t.Errorf(diff)
			}
			if diff := cmp.Diff(tc.expectedControlPlaneConfig, string(cloudConfig.Data["azure.json"])); diff != "" {
				t.Errorf(diff)
			}
		})
	}
}

func TestReconcileAzureSecret(t *testing.T) {
	g := NewWithT(t)

	cases := map[string]struct {
		kind             string
		apiVersion       string
		ownerName        string
		existingSecret   *corev1.Secret
		expectedNoChange bool
	}{
		"azuremachine should reconcile secret successfully": {
			kind:       "AzureMachine",
			apiVersion: "infrastructure.cluster.x-k8s.io/v1beta1",
			ownerName:  "azureMachineName",
		},
		"azuremachinepool should reconcile secret successfully": {
			kind:       "AzureMachinePool",
			apiVersion: "infrastructure.cluster.x-k8s.io/v1beta1",
			ownerName:  "azureMachinePoolName",
		},
		"azuremachinetemplate should reconcile secret successfully": {
			kind:       "AzureMachineTemplate",
			apiVersion: "infrastructure.cluster.x-k8s.io/v1beta1",
			ownerName:  "azureMachineTemplateName",
		},
		"should not replace the content of the pre-existing unowned secret": {
			kind:       "AzureMachine",
			apiVersion: "infrastructure.cluster.x-k8s.io/v1beta1",
			ownerName:  "azureMachineName",
			existingSecret: &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "azureMachineName-azure-json",
					Namespace: "default",
					Labels:    map[string]string{"testCluster": "foo"},
				},
				Data: map[string][]byte{
					"azure.json": []byte("foobar"),
				},
			},
			expectedNoChange: true,
		},
		"should not replace the content of the pre-existing unowned secret without the label": {
			kind:       "AzureMachine",
			apiVersion: "infrastructure.cluster.x-k8s.io/v1beta1",
			ownerName:  "azureMachineName",
			existingSecret: &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "azureMachineName-azure-json",
					Namespace: "default",
				},
				Data: map[string][]byte{
					"azure.json": []byte("foobar"),
				},
			},
			expectedNoChange: true,
		},
		"should replace the content of the pre-existing owned secret": {
			kind:       "AzureMachine",
			apiVersion: "infrastructure.cluster.x-k8s.io/v1beta1",
			ownerName:  "azureMachineName",
			existingSecret: &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "azureMachineName-azure-json",
					Namespace: "default",
					Labels:    map[string]string{"testCluster": string(infrav1.ResourceLifecycleOwned)},
				},
				Data: map[string][]byte{
					"azure.json": []byte("foobar"),
				},
			},
		},
	}

	cluster := newCluster("foo")
	azureCluster := newAzureCluster("bar")

	azureCluster.Default()
	cluster.Name = "testCluster"

	scheme := setupScheme(g)
	kubeclient := fake.NewClientBuilder().WithScheme(scheme).Build()

	clusterScope, err := scope.NewClusterScope(context.Background(), scope.ClusterScopeParams{
		AzureClients: scope.AzureClients{
			Authorizer: autorest.NullAuthorizer{},
		},
		Cluster:      cluster,
		AzureCluster: azureCluster,
		Client:       kubeclient,
	})
	g.Expect(err).NotTo(HaveOccurred())

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if tc.existingSecret != nil {
				_ = kubeclient.Delete(context.Background(), tc.existingSecret)
				_ = kubeclient.Create(context.Background(), tc.existingSecret)
				defer func() {
					_ = kubeclient.Delete(context.Background(), tc.existingSecret)
				}()
			}

			owner := metav1.OwnerReference{
				APIVersion: tc.apiVersion,
				Kind:       tc.kind,
				Name:       tc.ownerName,
			}
			cloudConfig, err := GetCloudProviderSecret(clusterScope, "default", tc.ownerName, owner, infrav1.VMIdentitySystemAssigned, "")
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(cloudConfig.Data).NotTo(BeNil())

			if err := reconcileAzureSecret(context.Background(), kubeclient, owner, cloudConfig, cluster.Name); err != nil {
				t.Error(err)
			}

			key := types.NamespacedName{
				Namespace: "default",
				Name:      fmt.Sprintf("%s-azure-json", tc.ownerName),
			}
			found := &corev1.Secret{}
			if err := kubeclient.Get(context.Background(), key, found); err != nil {
				t.Error(err)
			}

			if tc.expectedNoChange {
				g.Expect(cloudConfig.Data).NotTo(Equal(found.Data))
			} else {
				g.Expect(cloudConfig.Data).To(Equal(found.Data))
				g.Expect(found.OwnerReferences).To(Equal(cloudConfig.OwnerReferences))
			}
		})
	}
}

func setupScheme(g *WithT) *runtime.Scheme {
	scheme := runtime.NewScheme()
	g.Expect(clientgoscheme.AddToScheme(scheme)).To(Succeed())
	g.Expect(infrav1.AddToScheme(scheme)).To(Succeed())
	g.Expect(clusterv1.AddToScheme(scheme)).To(Succeed())
	return scheme
}

func newMachine(clusterName, machineName string) *clusterv1.Machine {
	return &clusterv1.Machine{
		ObjectMeta: metav1.ObjectMeta{
			Labels: map[string]string{
				clusterv1.ClusterNameLabel: clusterName,
			},
			Name:      machineName,
			Namespace: "default",
		},
	}
}

func newMachineWithInfrastructureRef(clusterName, machineName string) *clusterv1.Machine {
	m := newMachine(clusterName, machineName)
	m.Spec.InfrastructureRef = corev1.ObjectReference{
		Kind:       "AzureMachine",
		Namespace:  "default",
		Name:       "azure" + machineName,
		APIVersion: infrav1.GroupVersion.String(),
	}
	return m
}

func newCluster(name string) *clusterv1.Cluster {
	return &clusterv1.Cluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: "default",
		},
	}
}

func newAzureCluster(location string) *infrav1.AzureCluster {
	return &infrav1.AzureCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "foo",
			Namespace: "default",
		},
		Spec: infrav1.AzureClusterSpec{
			AzureClusterClassSpec: infrav1.AzureClusterClassSpec{
				Location:       location,
				SubscriptionID: "baz",
			},
			NetworkSpec: infrav1.NetworkSpec{
				Vnet: infrav1.VnetSpec{},
			},
			ResourceGroup: "bar",
		},
	}
}

func withRateLimits(ac infrav1.AzureCluster) *infrav1.AzureCluster {
	cloudProviderRateLimitQPS := resource.MustParse("1.2")
	rateLimits := []infrav1.RateLimitSpec{
		{
			Name: "defaultRateLimit",
			Config: infrav1.RateLimitConfig{
				CloudProviderRateLimit:    true,
				CloudProviderRateLimitQPS: &cloudProviderRateLimitQPS,
			},
		},
		{
			Name: "loadBalancerRateLimit",
			Config: infrav1.RateLimitConfig{
				CloudProviderRateLimitBucket: 10,
			},
		},
	}
	ac.Spec.CloudProviderConfigOverrides = &infrav1.CloudProviderConfigOverrides{RateLimits: rateLimits}
	return &ac
}

func withbackOffConfig(ac infrav1.AzureCluster) *infrav1.AzureCluster {
	cloudProviderBackOffExponent := resource.MustParse("1.2")
	backOff := infrav1.BackOffConfig{
		CloudProviderBackoff:         true,
		CloudProviderBackoffRetries:  1,
		CloudProviderBackoffExponent: &cloudProviderBackOffExponent,
		CloudProviderBackoffDuration: 60,
		CloudProviderBackoffJitter:   &cloudProviderBackOffExponent,
	}
	ac.Spec.CloudProviderConfigOverrides = &infrav1.CloudProviderConfigOverrides{BackOffs: backOff}
	return &ac
}

func newAzureClusterWithCustomVnet(location string) *infrav1.AzureCluster {
	return &infrav1.AzureCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "foo",
			Namespace: "default",
		},
		Spec: infrav1.AzureClusterSpec{
			AzureClusterClassSpec: infrav1.AzureClusterClassSpec{
				Location:       location,
				SubscriptionID: "baz",
			},
			NetworkSpec: infrav1.NetworkSpec{
				Vnet: infrav1.VnetSpec{
					Name:          "custom-vnet",
					ResourceGroup: "custom-vnet-resource-group",
				},
				Subnets: infrav1.Subnets{
					infrav1.SubnetSpec{
						SubnetClassSpec: infrav1.SubnetClassSpec{
							Name: "foo-controlplane-subnet",
							Role: infrav1.SubnetControlPlane,
						},
					},
					infrav1.SubnetSpec{
						SubnetClassSpec: infrav1.SubnetClassSpec{
							Name: "foo-node-subnet",
							Role: infrav1.SubnetNode,
						},
					},
				},
			},
			ResourceGroup: "bar",
		},
	}
}

const (
	spControlPlaneCloudConfig = `{
    "cloud": "AzurePublicCloud",
    "tenantId": "fooTenant",
    "subscriptionId": "baz",
    "aadClientId": "fooClient",
    "aadClientSecret": "fooSecret",
    "resourceGroup": "bar",
    "securityGroupName": "foo-node-nsg",
    "securityGroupResourceGroup": "bar",
    "location": "bar",
    "vmType": "vmss",
    "vnetName": "foo-vnet",
    "vnetResourceGroup": "bar",
    "subnetName": "foo-node-subnet",
    "routeTableName": "foo-node-routetable",
    "loadBalancerSku": "Standard",
    "loadBalancerName": "",
    "maximumLoadBalancerRuleCount": 250,
    "useManagedIdentityExtension": false,
    "useInstanceMetadata": true
}`
	//nolint:gosec // Ignore "G101: Potential hardcoded credentials" check.
	spWorkerNodeCloudConfig = `{
    "cloud": "AzurePublicCloud",
    "tenantId": "fooTenant",
    "subscriptionId": "baz",
    "aadClientId": "fooClient",
    "aadClientSecret": "fooSecret",
    "resourceGroup": "bar",
    "securityGroupName": "foo-node-nsg",
    "securityGroupResourceGroup": "bar",
    "location": "bar",
    "vmType": "vmss",
    "vnetName": "foo-vnet",
    "vnetResourceGroup": "bar",
    "subnetName": "foo-node-subnet",
    "routeTableName": "foo-node-routetable",
    "loadBalancerSku": "Standard",
    "loadBalancerName": "",
    "maximumLoadBalancerRuleCount": 250,
    "useManagedIdentityExtension": false,
    "useInstanceMetadata": true
}`

	systemAssignedControlPlaneCloudConfig = `{
    "cloud": "AzurePublicCloud",
    "tenantId": "fooTenant",
    "subscriptionId": "baz",
    "resourceGroup": "bar",
    "securityGroupName": "foo-node-nsg",
    "securityGroupResourceGroup": "bar",
    "location": "bar",
    "vmType": "vmss",
    "vnetName": "foo-vnet",
    "vnetResourceGroup": "bar",
    "subnetName": "foo-node-subnet",
    "routeTableName": "foo-node-routetable",
    "loadBalancerSku": "Standard",
    "loadBalancerName": "",
    "maximumLoadBalancerRuleCount": 250,
    "useManagedIdentityExtension": true,
    "useInstanceMetadata": true
}`
	systemAssignedWorkerNodeCloudConfig = `{
    "cloud": "AzurePublicCloud",
    "tenantId": "fooTenant",
    "subscriptionId": "baz",
    "resourceGroup": "bar",
    "securityGroupName": "foo-node-nsg",
    "securityGroupResourceGroup": "bar",
    "location": "bar",
    "vmType": "vmss",
    "vnetName": "foo-vnet",
    "vnetResourceGroup": "bar",
    "subnetName": "foo-node-subnet",
    "routeTableName": "foo-node-routetable",
    "loadBalancerSku": "Standard",
    "loadBalancerName": "",
    "maximumLoadBalancerRuleCount": 250,
    "useManagedIdentityExtension": true,
    "useInstanceMetadata": true
}`

	userAssignedControlPlaneCloudConfig = `{
    "cloud": "AzurePublicCloud",
    "tenantId": "fooTenant",
    "subscriptionId": "baz",
    "resourceGroup": "bar",
    "securityGroupName": "foo-node-nsg",
    "securityGroupResourceGroup": "bar",
    "location": "bar",
    "vmType": "vmss",
    "vnetName": "foo-vnet",
    "vnetResourceGroup": "bar",
    "subnetName": "foo-node-subnet",
    "routeTableName": "foo-node-routetable",
    "loadBalancerSku": "Standard",
    "loadBalancerName": "",
    "maximumLoadBalancerRuleCount": 250,
    "useManagedIdentityExtension": true,
    "useInstanceMetadata": true,
    "userAssignedIdentityID": "foobar"
}`
	userAssignedWorkerNodeCloudConfig = `{
    "cloud": "AzurePublicCloud",
    "tenantId": "fooTenant",
    "subscriptionId": "baz",
    "resourceGroup": "bar",
    "securityGroupName": "foo-node-nsg",
    "securityGroupResourceGroup": "bar",
    "location": "bar",
    "vmType": "vmss",
    "vnetName": "foo-vnet",
    "vnetResourceGroup": "bar",
    "subnetName": "foo-node-subnet",
    "routeTableName": "foo-node-routetable",
    "loadBalancerSku": "Standard",
    "loadBalancerName": "",
    "maximumLoadBalancerRuleCount": 250,
    "useManagedIdentityExtension": true,
    "useInstanceMetadata": true,
    "userAssignedIdentityID": "foobar"
}`
	spCustomVnetControlPlaneCloudConfig = `{
    "cloud": "AzurePublicCloud",
    "tenantId": "fooTenant",
    "subscriptionId": "baz",
    "aadClientId": "fooClient",
    "aadClientSecret": "fooSecret",
    "resourceGroup": "bar",
    "securityGroupName": "foo-node-nsg",
    "securityGroupResourceGroup": "custom-vnet-resource-group",
    "location": "bar",
    "vmType": "vmss",
    "vnetName": "custom-vnet",
    "vnetResourceGroup": "custom-vnet-resource-group",
    "subnetName": "foo-node-subnet",
    "routeTableName": "foo-node-routetable",
    "loadBalancerSku": "Standard",
    "loadBalancerName": "",
    "maximumLoadBalancerRuleCount": 250,
    "useManagedIdentityExtension": false,
    "useInstanceMetadata": true
}`
	spCustomVnetWorkerNodeCloudConfig = `{
    "cloud": "AzurePublicCloud",
    "tenantId": "fooTenant",
    "subscriptionId": "baz",
    "aadClientId": "fooClient",
    "aadClientSecret": "fooSecret",
    "resourceGroup": "bar",
    "securityGroupName": "foo-node-nsg",
    "securityGroupResourceGroup": "custom-vnet-resource-group",
    "location": "bar",
    "vmType": "vmss",
    "vnetName": "custom-vnet",
    "vnetResourceGroup": "custom-vnet-resource-group",
    "subnetName": "foo-node-subnet",
    "routeTableName": "foo-node-routetable",
    "loadBalancerSku": "Standard",
    "loadBalancerName": "",
    "maximumLoadBalancerRuleCount": 250,
    "useManagedIdentityExtension": false,
    "useInstanceMetadata": true
}`
	rateLimitsControlPlaneCloudConfig = `{
    "cloud": "AzurePublicCloud",
    "tenantId": "fooTenant",
    "subscriptionId": "baz",
    "aadClientId": "fooClient",
    "aadClientSecret": "fooSecret",
    "resourceGroup": "bar",
    "securityGroupName": "foo-node-nsg",
    "securityGroupResourceGroup": "bar",
    "location": "bar",
    "vmType": "vmss",
    "vnetName": "foo-vnet",
    "vnetResourceGroup": "bar",
    "subnetName": "foo-node-subnet",
    "routeTableName": "foo-node-routetable",
    "loadBalancerSku": "Standard",
    "loadBalancerName": "",
    "maximumLoadBalancerRuleCount": 250,
    "useManagedIdentityExtension": false,
    "useInstanceMetadata": true,
    "cloudProviderRateLimit": true,
    "cloudProviderRateLimitQPS": 1.2,
    "loadBalancerRateLimit": {
        "cloudProviderRateLimitBucket": 10
    }
}`
	rateLimitsWorkerNodeCloudConfig = `{
    "cloud": "AzurePublicCloud",
    "tenantId": "fooTenant",
    "subscriptionId": "baz",
    "aadClientId": "fooClient",
    "aadClientSecret": "fooSecret",
    "resourceGroup": "bar",
    "securityGroupName": "foo-node-nsg",
    "securityGroupResourceGroup": "bar",
    "location": "bar",
    "vmType": "vmss",
    "vnetName": "foo-vnet",
    "vnetResourceGroup": "bar",
    "subnetName": "foo-node-subnet",
    "routeTableName": "foo-node-routetable",
    "loadBalancerSku": "Standard",
    "loadBalancerName": "",
    "maximumLoadBalancerRuleCount": 250,
    "useManagedIdentityExtension": false,
    "useInstanceMetadata": true,
    "cloudProviderRateLimit": true,
    "cloudProviderRateLimitQPS": 1.2,
    "loadBalancerRateLimit": {
        "cloudProviderRateLimitBucket": 10
    }
}`
	backOffCloudConfig = `{
    "cloud": "AzurePublicCloud",
    "tenantId": "fooTenant",
    "subscriptionId": "baz",
    "aadClientId": "fooClient",
    "aadClientSecret": "fooSecret",
    "resourceGroup": "bar",
    "securityGroupName": "foo-node-nsg",
    "securityGroupResourceGroup": "bar",
    "location": "bar",
    "vmType": "vmss",
    "vnetName": "foo-vnet",
    "vnetResourceGroup": "bar",
    "subnetName": "foo-node-subnet",
    "routeTableName": "foo-node-routetable",
    "loadBalancerSku": "Standard",
    "loadBalancerName": "",
    "maximumLoadBalancerRuleCount": 250,
    "useManagedIdentityExtension": false,
    "useInstanceMetadata": true,
    "cloudProviderBackoff": true,
    "cloudProviderBackoffRetries": 1,
    "cloudProviderBackoffExponent": 1.2000000000000002,
    "cloudProviderBackoffDuration": 60,
    "cloudProviderBackoffJitter": 1.2000000000000002
}`
	vmssCloudConfig = `{
    "cloud": "AzurePublicCloud",
    "tenantId": "fooTenant",
    "subscriptionId": "baz",
    "aadClientId": "fooClient",
    "aadClientSecret": "fooSecret",
    "resourceGroup": "bar",
    "securityGroupName": "foo-node-nsg",
    "securityGroupResourceGroup": "bar",
    "location": "bar",
    "vmType": "vmss",
    "vnetName": "foo-vnet",
    "vnetResourceGroup": "bar",
    "subnetName": "foo-node-subnet",
    "routeTableName": "foo-node-routetable",
    "loadBalancerSku": "Standard",
    "loadBalancerName": "",
    "maximumLoadBalancerRuleCount": 250,
    "useManagedIdentityExtension": false,
    "useInstanceMetadata": true,
    "enableVmssFlexNodes": true
}`
)

func Test_clusterIdentityFinalizer(t *testing.T) {
	type args struct {
		prefix           string
		clusterNamespace string
		clusterName      string
	}
	tests := []struct {
		name string
		args args
		want string
	}{
		{
			name: "cluster identity finalizer should be deterministic",
			args: args{
				prefix:           infrav1.ClusterFinalizer,
				clusterNamespace: "foo",
				clusterName:      "bar",
			},
			want: "azurecluster.infrastructure.cluster.x-k8s.io/48998dbcd8fb929369c78981cbfb6f26145ea0412e6e05a1423941a6",
		},
		{
			name: "long cluster name and namespace",
			args: args{
				prefix:           infrav1.ClusterFinalizer,
				clusterNamespace: "this-is-a-very-very-very-very-very-very-very-very-very-long-namespace-name",
				clusterName:      "this-is-a-very-very-very-very-very-very-very-very-very-long-cluster-name",
			},
			want: "azurecluster.infrastructure.cluster.x-k8s.io/557d064144d2b495db694dedc53c9a1e9bd8575bdf06b5b151972614",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := clusterIdentityFinalizer(tt.args.prefix, tt.args.clusterNamespace, tt.args.clusterName)
			if got != tt.want {
				t.Errorf("clusterIdentityFinalizer() = %v, want %v", got, tt.want)
			}
			key := strings.Split(got, "/")[1]
			if len(key) > 63 {
				t.Errorf("clusterIdentityFinalizer() name %v length = %v should be less than 63 characters", key, len(key))
			}
		})
	}
}

func Test_deprecatedClusterIdentityFinalizer(t *testing.T) {
	type args struct {
		prefix           string
		clusterNamespace string
		clusterName      string
	}
	tests := []struct {
		name string
		args args
		want string
	}{
		{
			name: "cluster identity finalizer should be deterministic",
			args: args{
				prefix:           infrav1.ClusterFinalizer,
				clusterNamespace: "foo",
				clusterName:      "bar",
			},
			want: "azurecluster.infrastructure.cluster.x-k8s.io/foo-bar",
		},
		{
			name: "long cluster name and namespace",
			args: args{
				prefix:           infrav1.ClusterFinalizer,
				clusterNamespace: "this-is-a-very-very-very-very-very-very-very-very-very-long-namespace-name",
				clusterName:      "this-is-a-very-very-very-very-very-very-very-very-very-long-cluster-name",
			},
			want: "azurecluster.infrastructure.cluster.x-k8s.io/this-is-a-very-very-very-very-very-very-very-very-very-long-namespace-name-this-is-a-very-very-very-very-very-very-very-very-very-long-cluster-name",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := deprecatedClusterIdentityFinalizer(tt.args.prefix, tt.args.clusterNamespace, tt.args.clusterName); got != tt.want {
				t.Errorf("deprecatedClusterIdentityFinalizer() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestAzureManagedClusterToAzureManagedMachinePoolsMapper(t *testing.T) {
	g := NewWithT(t)
	scheme, err := newScheme()
	g.Expect(err).NotTo(HaveOccurred())
	initObjects := []runtime.Object{
		newCluster(clusterName),
		// Create two Machines with an infrastructure ref and one without.
		newManagedMachinePoolInfraReference(clusterName, "my-mmp-0"),
		newManagedMachinePoolInfraReference(clusterName, "my-mmp-1"),
		newManagedMachinePoolInfraReference(clusterName, "my-mmp-2"),
		newMachinePool(clusterName, "my-machine-2"),
	}
	fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithRuntimeObjects(initObjects...).Build()

	sink := mock_log.NewMockLogSink(gomock.NewController(t))
	sink.EXPECT().Init(logr.RuntimeInfo{CallDepth: 1})
	sink.EXPECT().Enabled(4).Return(true)
	sink.EXPECT().WithValues("AzureManagedCluster", "my-cluster", "Namespace", "default").Return(sink)
	sink.EXPECT().Info(4, "gk does not match", "gk", gomock.Any(), "infraGK", gomock.Any())
	mapper, err := AzureManagedClusterToAzureManagedMachinePoolsMapper(context.Background(), fakeClient, scheme, logr.New(sink))
	g.Expect(err).NotTo(HaveOccurred())

	requests := mapper(&infrav1.AzureManagedCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      clusterName,
			Namespace: "default",
			OwnerReferences: []metav1.OwnerReference{
				{
					Name:       clusterName,
					Kind:       "Cluster",
					APIVersion: clusterv1.GroupVersion.String(),
				},
			},
		},
	})
	g.Expect(requests).To(ConsistOf([]reconcile.Request{
		{
			NamespacedName: types.NamespacedName{
				Name:      "azuremy-mmp-0",
				Namespace: "default",
			},
		},
		{
			NamespacedName: types.NamespacedName{
				Name:      "azuremy-mmp-1",
				Namespace: "default",
			},
		},
		{
			NamespacedName: types.NamespacedName{
				Name:      "azuremy-mmp-2",
				Namespace: "default",
			},
		},
	}))
}

func TestAzureManagedControlPlaneToAzureManagedMachinePoolsMapper(t *testing.T) {
	g := NewWithT(t)
	scheme, err := newScheme()
	g.Expect(err).NotTo(HaveOccurred())
	cluster := newCluster("my-cluster")
	cluster.Spec.ControlPlaneRef = &corev1.ObjectReference{
		APIVersion: infrav1.GroupVersion.String(),
		Kind:       "AzureManagedControlPlane",
		Name:       cpName,
		Namespace:  cluster.Namespace,
	}
	initObjects := []runtime.Object{
		cluster,
		newAzureManagedControlPlane(cpName),
		// Create two Machines with an infrastructure ref and one without.
		newManagedMachinePoolInfraReference(clusterName, "my-mmp-0"),
		newManagedMachinePoolInfraReference(clusterName, "my-mmp-1"),
		newManagedMachinePoolInfraReference(clusterName, "my-mmp-2"),
		newMachinePool(clusterName, "my-machine-2"),
	}
	fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithRuntimeObjects(initObjects...).Build()

	sink := mock_log.NewMockLogSink(gomock.NewController(t))
	sink.EXPECT().Init(logr.RuntimeInfo{CallDepth: 1})
	sink.EXPECT().Enabled(4).Return(true)
	sink.EXPECT().WithValues("AzureManagedControlPlane", cpName, "Namespace", cluster.Namespace).Return(sink)
	sink.EXPECT().Info(4, "gk does not match", "gk", gomock.Any(), "infraGK", gomock.Any())
	mapper, err := AzureManagedControlPlaneToAzureManagedMachinePoolsMapper(context.Background(), fakeClient, scheme, logr.New(sink))
	g.Expect(err).NotTo(HaveOccurred())

	requests := mapper(&infrav1.AzureManagedControlPlane{
		ObjectMeta: metav1.ObjectMeta{
			Name:      cpName,
			Namespace: cluster.Namespace,
			OwnerReferences: []metav1.OwnerReference{
				{
					Name:       cluster.Name,
					Kind:       "Cluster",
					APIVersion: clusterv1.GroupVersion.String(),
				},
			},
		},
	})
	g.Expect(requests).To(ConsistOf([]reconcile.Request{
		{
			NamespacedName: types.NamespacedName{
				Name:      "azuremy-mmp-0",
				Namespace: "default",
			},
		},
		{
			NamespacedName: types.NamespacedName{
				Name:      "azuremy-mmp-1",
				Namespace: "default",
			},
		},
		{
			NamespacedName: types.NamespacedName{
				Name:      "azuremy-mmp-2",
				Namespace: "default",
			},
		},
	}))
}

func TestMachinePoolToAzureManagedControlPlaneMapFuncSuccess(t *testing.T) {
	g := NewWithT(t)
	scheme, err := newScheme()
	g.Expect(err).NotTo(HaveOccurred())
	cluster := newCluster(clusterName)
	controlPlane := newAzureManagedControlPlane(cpName)
	cluster.Spec.ControlPlaneRef = &corev1.ObjectReference{
		APIVersion: infrav1.GroupVersion.String(),
		Kind:       "AzureManagedControlPlane",
		Name:       cpName,
		Namespace:  cluster.Namespace,
	}

	managedMachinePool0 := newManagedMachinePoolInfraReference(clusterName, "my-mmp-0")
	azureManagedMachinePool0 := newAzureManagedMachinePool(clusterName, "azuremy-mmp-0", "System")
	managedMachinePool0.Spec.ClusterName = clusterName

	managedMachinePool1 := newManagedMachinePoolInfraReference(clusterName, "my-mmp-1")
	azureManagedMachinePool1 := newAzureManagedMachinePool(clusterName, "azuremy-mmp-1", "User")
	managedMachinePool1.Spec.ClusterName = clusterName

	initObjects := []runtime.Object{
		cluster,
		controlPlane,
		managedMachinePool0,
		azureManagedMachinePool0,
		// Create two Machines with an infrastructure ref and one without.
		managedMachinePool1,
		azureManagedMachinePool1,
	}
	fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithRuntimeObjects(initObjects...).Build()

	sink := mock_log.NewMockLogSink(gomock.NewController(t))
	sink.EXPECT().Init(logr.RuntimeInfo{CallDepth: 1})
	mapper := MachinePoolToAzureManagedControlPlaneMapFunc(context.Background(), fakeClient, infrav1.GroupVersion.WithKind("AzureManagedControlPlane"), logr.New(sink))

	// system pool should trigger
	requests := mapper(newManagedMachinePoolInfraReference(clusterName, "my-mmp-0"))
	g.Expect(requests).To(ConsistOf([]reconcile.Request{
		{
			NamespacedName: types.NamespacedName{
				Name:      "my-managed-cp",
				Namespace: "default",
			},
		},
	}))

	// any other pool should not trigger
	requests = mapper(newManagedMachinePoolInfraReference(clusterName, "my-mmp-1"))
	g.Expect(requests).To(BeNil())
}

func TestMachinePoolToAzureManagedControlPlaneMapFuncFailure(t *testing.T) {
	g := NewWithT(t)
	scheme, err := newScheme()
	g.Expect(err).NotTo(HaveOccurred())
	cluster := newCluster(clusterName)
	cluster.Spec.ControlPlaneRef = &corev1.ObjectReference{
		APIVersion: infrav1.GroupVersion.String(),
		Kind:       "AzureManagedControlPlane",
		Name:       cpName,
		Namespace:  cluster.Namespace,
	}
	managedMachinePool := newManagedMachinePoolInfraReference(clusterName, "my-mmp-0")
	managedMachinePool.Spec.ClusterName = clusterName
	initObjects := []runtime.Object{
		cluster,
		managedMachinePool,
		// Create two Machines with an infrastructure ref and one without.
		newManagedMachinePoolInfraReference(clusterName, "my-mmp-1"),
	}
	fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithRuntimeObjects(initObjects...).Build()

	sink := mock_log.NewMockLogSink(gomock.NewController(t))
	sink.EXPECT().Init(logr.RuntimeInfo{CallDepth: 1})
	sink.EXPECT().Error(gomock.Any(), "failed to fetch default pool reference")
	sink.EXPECT().Error(gomock.Any(), "failed to fetch default pool reference") // twice because we are testing two calls

	mapper := MachinePoolToAzureManagedControlPlaneMapFunc(context.Background(), fakeClient, infrav1.GroupVersion.WithKind("AzureManagedControlPlane"), logr.New(sink))

	// default pool should trigger if owned cluster could not be fetched
	requests := mapper(newManagedMachinePoolInfraReference(clusterName, "my-mmp-0"))
	g.Expect(requests).To(ConsistOf([]reconcile.Request{
		{
			NamespacedName: types.NamespacedName{
				Name:      "my-managed-cp",
				Namespace: "default",
			},
		},
	}))

	// any other pool should also trigger if owned cluster could not be fetched
	requests = mapper(newManagedMachinePoolInfraReference(clusterName, "my-mmp-1"))
	g.Expect(requests).To(ConsistOf([]reconcile.Request{
		{
			NamespacedName: types.NamespacedName{
				Name:      "my-managed-cp",
				Namespace: "default",
			},
		},
	}))
}

func TestAzureManagedClusterToAzureManagedControlPlaneMapper(t *testing.T) {
	g := NewWithT(t)
	scheme, err := newScheme()
	g.Expect(err).NotTo(HaveOccurred())
	cluster := newCluster("my-cluster")
	cluster.Spec.ControlPlaneRef = &corev1.ObjectReference{
		APIVersion: infrav1.GroupVersion.String(),
		Kind:       "AzureManagedControlPlane",
		Name:       cpName,
		Namespace:  cluster.Namespace,
	}

	initObjects := []runtime.Object{
		cluster,
		newAzureManagedControlPlane(cpName),
	}
	fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithRuntimeObjects(initObjects...).Build()

	sink := mock_log.NewMockLogSink(gomock.NewController(t))
	sink.EXPECT().Init(logr.RuntimeInfo{CallDepth: 1})
	sink.EXPECT().WithValues("AzureManagedCluster", "az-"+cluster.Name, "Namespace", "default")

	mapper, err := AzureManagedClusterToAzureManagedControlPlaneMapper(context.Background(), fakeClient, logr.New(sink))
	g.Expect(err).NotTo(HaveOccurred())
	requests := mapper(&infrav1.AzureManagedCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "az-" + cluster.Name,
			Namespace: "default",
			OwnerReferences: []metav1.OwnerReference{
				{
					Name:       cluster.Name,
					Kind:       "Cluster",
					APIVersion: clusterv1.GroupVersion.String(),
				},
			},
		},
	})
	g.Expect(requests).To(HaveLen(1))
	g.Expect(requests).To(Equal([]reconcile.Request{
		{
			NamespacedName: types.NamespacedName{
				Name:      cpName,
				Namespace: cluster.Namespace,
			},
		},
	}))
}

func TestAzureManagedControlPlaneToAzureManagedClusterMapper(t *testing.T) {
	g := NewWithT(t)
	scheme, err := newScheme()
	g.Expect(err).NotTo(HaveOccurred())
	cluster := newCluster("my-cluster")
	azManagedCluster := &infrav1.AzureManagedCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "az-" + cluster.Name,
			Namespace: "default",
			OwnerReferences: []metav1.OwnerReference{
				{
					Name:       cluster.Name,
					Kind:       "Cluster",
					APIVersion: clusterv1.GroupVersion.String(),
				},
			},
		},
	}

	cluster.Spec.ControlPlaneRef = &corev1.ObjectReference{
		APIVersion: infrav1.GroupVersion.String(),
		Kind:       "AzureManagedControlPlane",
		Name:       cpName,
		Namespace:  cluster.Namespace,
	}
	cluster.Spec.InfrastructureRef = &corev1.ObjectReference{
		APIVersion: infrav1.GroupVersion.String(),
		Kind:       "AzureManagedCluster",
		Name:       azManagedCluster.Name,
		Namespace:  azManagedCluster.Namespace,
	}

	initObjects := []runtime.Object{
		cluster,
		newAzureManagedControlPlane(cpName),
		azManagedCluster,
	}
	fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithRuntimeObjects(initObjects...).Build()

	sink := mock_log.NewMockLogSink(gomock.NewController(t))
	sink.EXPECT().Init(logr.RuntimeInfo{CallDepth: 1})
	sink.EXPECT().WithValues("AzureManagedControlPlane", cpName, "Namespace", cluster.Namespace)

	mapper, err := AzureManagedControlPlaneToAzureManagedClusterMapper(context.Background(), fakeClient, logr.New(sink))
	g.Expect(err).NotTo(HaveOccurred())
	requests := mapper(&infrav1.AzureManagedControlPlane{
		ObjectMeta: metav1.ObjectMeta{
			Name:      cpName,
			Namespace: cluster.Namespace,
			OwnerReferences: []metav1.OwnerReference{
				{
					Name:       cluster.Name,
					Kind:       "Cluster",
					APIVersion: clusterv1.GroupVersion.String(),
				},
			},
		},
	})
	g.Expect(requests).To(HaveLen(1))
	g.Expect(requests).To(Equal([]reconcile.Request{
		{
			NamespacedName: types.NamespacedName{
				Name:      azManagedCluster.Name,
				Namespace: azManagedCluster.Namespace,
			},
		},
	}))
}

func newAzureManagedControlPlane(cpName string) *infrav1.AzureManagedControlPlane {
	return &infrav1.AzureManagedControlPlane{
		ObjectMeta: metav1.ObjectMeta{
			Name:      cpName,
			Namespace: "default",
		},
	}
}

func newManagedMachinePoolInfraReference(clusterName, poolName string) *expv1.MachinePool {
	m := newMachinePool(clusterName, poolName)
	m.Spec.ClusterName = clusterName
	m.Spec.Template.Spec.InfrastructureRef = corev1.ObjectReference{
		Kind:       "AzureManagedMachinePool",
		Namespace:  m.Namespace,
		Name:       "azure" + poolName,
		APIVersion: infrav1.GroupVersion.String(),
	}
	return m
}

func newAzureManagedMachinePool(clusterName, poolName, mode string) *infrav1.AzureManagedMachinePool {
	var cpuManagerPolicyStatic = infrav1.CPUManagerPolicyStatic
	var topologyManagerPolicy = infrav1.TopologyManagerPolicyBestEffort
	var transparentHugePageDefragMAdvise = infrav1.TransparentHugePageOptionMadvise
	var transparentHugePageEnabledAlways = infrav1.TransparentHugePageOptionAlways
	return &infrav1.AzureManagedMachinePool{
		ObjectMeta: metav1.ObjectMeta{
			Labels: map[string]string{
				clusterv1.ClusterNameLabel: clusterName,
			},
			Name:      poolName,
			Namespace: "default",
		},
		Spec: infrav1.AzureManagedMachinePoolSpec{
			Mode:         mode,
			SKU:          "Standard_B2s",
			OSDiskSizeGB: pointer.Int32(512),
			KubeletConfig: &infrav1.KubeletConfig{
				CPUManagerPolicy:      &cpuManagerPolicyStatic,
				TopologyManagerPolicy: &topologyManagerPolicy,
			},
			LinuxOSConfig: &infrav1.LinuxOSConfig{
				TransparentHugePageDefrag:  &transparentHugePageDefragMAdvise,
				TransparentHugePageEnabled: &transparentHugePageEnabledAlways,
			},
		},
	}
}

func newMachinePool(clusterName, poolName string) *expv1.MachinePool {
	return &expv1.MachinePool{
		ObjectMeta: metav1.ObjectMeta{
			Labels: map[string]string{
				clusterv1.ClusterNameLabel: clusterName,
			},
			Name:      poolName,
			Namespace: "default",
		},
		Spec: expv1.MachinePoolSpec{
			Replicas: pointer.Int32(2),
		},
	}
}

func newManagedMachinePoolWithInfrastructureRef(clusterName, poolName string) *expv1.MachinePool {
	m := newMachinePool(clusterName, poolName)
	m.Spec.Template.Spec.InfrastructureRef = corev1.ObjectReference{
		Kind:       "AzureManagedMachinePool",
		Namespace:  m.Namespace,
		Name:       "azure" + poolName,
		APIVersion: infrav1.GroupVersion.String(),
	}
	return m
}

func Test_ManagedMachinePoolToInfrastructureMapFunc(t *testing.T) {
	cases := []struct {
		Name             string
		Setup            func(logMock *mock_log.MockLogSink)
		MapObjectFactory func(*GomegaWithT) client.Object
		Expect           func(*GomegaWithT, []reconcile.Request)
	}{
		{
			Name: "MachinePoolToAzureManagedMachinePool",
			MapObjectFactory: func(g *GomegaWithT) client.Object {
				return newManagedMachinePoolWithInfrastructureRef("azureManagedCluster", "ManagedMachinePool")
			},
			Setup: func(logMock *mock_log.MockLogSink) {
				logMock.EXPECT().Init(logr.RuntimeInfo{CallDepth: 1})
			},
			Expect: func(g *GomegaWithT, reqs []reconcile.Request) {
				g.Expect(reqs).To(HaveLen(1))
				g.Expect(reqs[0]).To(Equal(reconcile.Request{
					NamespacedName: types.NamespacedName{
						Name:      "azureManagedMachinePool",
						Namespace: "default",
					},
				}))
			},
		},
		{
			Name: "MachinePoolWithoutMatchingInfraRef",
			MapObjectFactory: func(g *GomegaWithT) client.Object {
				return newMachinePool("azureManagedCluster", "machinePool")
			},
			Setup: func(logMock *mock_log.MockLogSink) {
				ampGK := infrav1.GroupVersion.WithKind("AzureManagedMachinePool").GroupKind()
				logMock.EXPECT().Init(logr.RuntimeInfo{CallDepth: 1})
				logMock.EXPECT().Enabled(4).Return(true)
				logMock.EXPECT().Info(4, "gk does not match", "gk", ampGK, "infraGK", gomock.Any())
			},
			Expect: func(g *GomegaWithT, reqs []reconcile.Request) {
				g.Expect(reqs).To(BeEmpty())
			},
		},
		{
			Name: "NotAMachinePool",
			MapObjectFactory: func(g *GomegaWithT) client.Object {
				return newCluster("azureManagedCluster")
			},
			Setup: func(logMock *mock_log.MockLogSink) {
				logMock.EXPECT().Init(logr.RuntimeInfo{CallDepth: 1})
				logMock.EXPECT().Enabled(4).Return(true)
				logMock.EXPECT().Info(4, "attempt to map incorrect type", "type", "*v1beta1.Cluster")
			},
			Expect: func(g *GomegaWithT, reqs []reconcile.Request) {
				g.Expect(reqs).To(BeEmpty())
			},
		},
	}

	for _, c := range cases {
		c := c
		t.Run(c.Name, func(t *testing.T) {
			g := NewWithT(t)

			mockCtrl := gomock.NewController(t)
			defer mockCtrl.Finish()

			sink := mock_log.NewMockLogSink(mockCtrl)
			if c.Setup != nil {
				c.Setup(sink)
			}
			f := MachinePoolToInfrastructureMapFunc(infrav1.GroupVersion.WithKind("AzureManagedMachinePool"), logr.New(sink))
			reqs := f(c.MapObjectFactory(g))
			c.Expect(g, reqs)
		})
	}
}
