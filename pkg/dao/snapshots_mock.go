// Code generated by mockery v2.20.0. DO NOT EDIT.

package dao

import (
	api "github.com/content-services/content-sources-backend/pkg/api"
	models "github.com/content-services/content-sources-backend/pkg/models"
	mock "github.com/stretchr/testify/mock"
)

// MockSnapshotDao is an autogenerated mock type for the SnapshotDao type
type MockSnapshotDao struct {
	mock.Mock
}

// Create provides a mock function with given fields: snap
func (_m *MockSnapshotDao) Create(snap *models.Snapshot) error {
	ret := _m.Called(snap)

	var r0 error
	if rf, ok := ret.Get(0).(func(*models.Snapshot) error); ok {
		r0 = rf(snap)
	} else {
		r0 = ret.Error(0)
	}

	return r0
}

// Delete provides a mock function with given fields: snapUUID
func (_m *MockSnapshotDao) Delete(snapUUID string) error {
	ret := _m.Called(snapUUID)

	var r0 error
	if rf, ok := ret.Get(0).(func(string) error); ok {
		r0 = rf(snapUUID)
	} else {
		r0 = ret.Error(0)
	}

	return r0
}

// FetchForRepoUUID provides a mock function with given fields: orgID, repoUUID
func (_m *MockSnapshotDao) FetchForRepoUUID(orgID string, repoUUID string) ([]models.Snapshot, error) {
	ret := _m.Called(orgID, repoUUID)

	var r0 []models.Snapshot
	var r1 error
	if rf, ok := ret.Get(0).(func(string, string) ([]models.Snapshot, error)); ok {
		return rf(orgID, repoUUID)
	}
	if rf, ok := ret.Get(0).(func(string, string) []models.Snapshot); ok {
		r0 = rf(orgID, repoUUID)
	} else {
		if ret.Get(0) != nil {
			r0 = ret.Get(0).([]models.Snapshot)
		}
	}

	if rf, ok := ret.Get(1).(func(string, string) error); ok {
		r1 = rf(orgID, repoUUID)
	} else {
		r1 = ret.Error(1)
	}

	return r0, r1
}

// List provides a mock function with given fields: repoConfigUuid, paginationData, filterData
func (_m *MockSnapshotDao) List(repoConfigUuid string, paginationData api.PaginationData, filterData api.FilterData) (api.SnapshotCollectionResponse, int64, error) {
	ret := _m.Called(repoConfigUuid, paginationData, filterData)

	var r0 api.SnapshotCollectionResponse
	var r1 int64
	var r2 error
	if rf, ok := ret.Get(0).(func(string, api.PaginationData, api.FilterData) (api.SnapshotCollectionResponse, int64, error)); ok {
		return rf(repoConfigUuid, paginationData, filterData)
	}
	if rf, ok := ret.Get(0).(func(string, api.PaginationData, api.FilterData) api.SnapshotCollectionResponse); ok {
		r0 = rf(repoConfigUuid, paginationData, filterData)
	} else {
		r0 = ret.Get(0).(api.SnapshotCollectionResponse)
	}

	if rf, ok := ret.Get(1).(func(string, api.PaginationData, api.FilterData) int64); ok {
		r1 = rf(repoConfigUuid, paginationData, filterData)
	} else {
		r1 = ret.Get(1).(int64)
	}

	if rf, ok := ret.Get(2).(func(string, api.PaginationData, api.FilterData) error); ok {
		r2 = rf(repoConfigUuid, paginationData, filterData)
	} else {
		r2 = ret.Error(2)
	}

	return r0, r1, r2
}

type mockConstructorTestingTNewMockSnapshotDao interface {
	mock.TestingT
	Cleanup(func())
}

// NewMockSnapshotDao creates a new instance of MockSnapshotDao. It also registers a testing interface on the mock and a cleanup function to assert the mocks expectations.
func NewMockSnapshotDao(t mockConstructorTestingTNewMockSnapshotDao) *MockSnapshotDao {
	mock := &MockSnapshotDao{}
	mock.Mock.Test(t)

	t.Cleanup(func() { mock.AssertExpectations(t) })

	return mock
}
