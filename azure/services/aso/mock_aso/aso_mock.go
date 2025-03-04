/*
Copyright The Kubernetes Authors.

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

// Code generated by MockGen. DO NOT EDIT.
// Source: ../interfaces.go

// Package mock_aso is a generated GoMock package.
package mock_aso

import (
	context "context"
	reflect "reflect"

	genruntime "github.com/Azure/azure-service-operator/v2/pkg/genruntime"
	gomock "github.com/golang/mock/gomock"
	azure "sigs.k8s.io/cluster-api-provider-azure/azure"
)

// MockReconciler is a mock of Reconciler interface.
type MockReconciler struct {
	ctrl     *gomock.Controller
	recorder *MockReconcilerMockRecorder
}

// MockReconcilerMockRecorder is the mock recorder for MockReconciler.
type MockReconcilerMockRecorder struct {
	mock *MockReconciler
}

// NewMockReconciler creates a new mock instance.
func NewMockReconciler(ctrl *gomock.Controller) *MockReconciler {
	mock := &MockReconciler{ctrl: ctrl}
	mock.recorder = &MockReconcilerMockRecorder{mock}
	return mock
}

// EXPECT returns an object that allows the caller to indicate expected use.
func (m *MockReconciler) EXPECT() *MockReconcilerMockRecorder {
	return m.recorder
}

// CreateOrUpdateResource mocks base method.
func (m *MockReconciler) CreateOrUpdateResource(ctx context.Context, spec azure.ASOResourceSpecGetter, serviceName string) (genruntime.MetaObject, error) {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "CreateOrUpdateResource", ctx, spec, serviceName)
	ret0, _ := ret[0].(genruntime.MetaObject)
	ret1, _ := ret[1].(error)
	return ret0, ret1
}

// CreateOrUpdateResource indicates an expected call of CreateOrUpdateResource.
func (mr *MockReconcilerMockRecorder) CreateOrUpdateResource(ctx, spec, serviceName interface{}) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "CreateOrUpdateResource", reflect.TypeOf((*MockReconciler)(nil).CreateOrUpdateResource), ctx, spec, serviceName)
}

// DeleteResource mocks base method.
func (m *MockReconciler) DeleteResource(ctx context.Context, spec azure.ASOResourceSpecGetter, serviceName string) error {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "DeleteResource", ctx, spec, serviceName)
	ret0, _ := ret[0].(error)
	return ret0
}

// DeleteResource indicates an expected call of DeleteResource.
func (mr *MockReconcilerMockRecorder) DeleteResource(ctx, spec, serviceName interface{}) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "DeleteResource", reflect.TypeOf((*MockReconciler)(nil).DeleteResource), ctx, spec, serviceName)
}
