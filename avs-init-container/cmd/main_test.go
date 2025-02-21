package main

import (
	"reflect"
	"testing"

	v1 "k8s.io/api/core/v1"
)

func TestGetNodeInstance(t *testing.T) {
	tests := []struct {
		name    string
		want    *v1.Node
		want1   *v1.Service
		wantErr bool
	}{
		// TODO: Add test cases.
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, got1, err := GetNodeInstance()
			if (err != nil) != tt.wantErr {
				t.Errorf("GetNodeInstance() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("GetNodeInstance() got = %v, want %v", got, tt.want)
			}
			if !reflect.DeepEqual(got1, tt.want1) {
				t.Errorf("GetNodeInstance() got1 = %v, want %v", got1, tt.want1)
			}
		})
	}
}

func Test_getEndpointByMode(t *testing.T) {
	tests := []struct {
		name    string
		want    string
		want1   int32
		wantErr bool
	}{
		// TODO: Add test cases.
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, got1, err := getEndpointByMode()
			if (err != nil) != tt.wantErr {
				t.Errorf("getEndpointByMode() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if got != tt.want {
				t.Errorf("getEndpointByMode() got = %v, want %v", got, tt.want)
			}
			if got1 != tt.want1 {
				t.Errorf("getEndpointByMode() got1 = %v, want %v", got1, tt.want1)
			}
		})
	}
}

func Test_setAdvertisedListeners(t *testing.T) {
	type args struct {
		aerospikeVectorSearchConfig map[string]interface{}
	}
	tests := []struct {
		name    string
		args    args
		wantErr bool
	}{
		// TODO: Add test cases.
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := setAdvertisedListeners(tt.args.aerospikeVectorSearchConfig); (err != nil) != tt.wantErr {
				t.Errorf("setAdvertisedListeners() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func Test_getNodeLabels(t *testing.T) {
	tests := []struct {
		name    string
		want    string
		wantErr bool
	}{
		// TODO: Add test cases.
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := getNodeLabels()
			if (err != nil) != tt.wantErr {
				t.Errorf("getNodeLabels() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if got != tt.want {
				t.Errorf("getNodeLabels() = %v, want %v", got, tt.want)
			}
		})
	}
}

func Test_getAerospikeVectorSearchRoles(t *testing.T) {
	tests := []struct {
		name    string
		want    map[string]interface{}
		wantErr bool
	}{
		// TODO: Add test cases.
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := getAerospikeVectorSearchRoles()
			if (err != nil) != tt.wantErr {
				t.Errorf("getAerospikeVectorSearchRoles() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("getAerospikeVectorSearchRoles() = %v, want %v", got, tt.want)
			}
		})
	}
}

func Test_setRoles(t *testing.T) {
	type args struct {
		aerospikeVectorSearchConfig map[string]interface{}
	}
	tests := []struct {
		name    string
		args    args
		wantErr bool
	}{
		// TODO: Add test cases.
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := setRoles(tt.args.aerospikeVectorSearchConfig); (err != nil) != tt.wantErr {
				t.Errorf("setRoles() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func Test_writeConfig(t *testing.T) {
	type args struct {
		aerospikeVectorSearchConfig map[string]interface{}
	}
	tests := []struct {
		name    string
		args    args
		wantErr bool
	}{
		// TODO: Add test cases.
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := writeConfig(tt.args.aerospikeVectorSearchConfig); (err != nil) != tt.wantErr {
				t.Errorf("writeConfig() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func Test_run(t *testing.T) {
	tests := []struct {
		name string
		want int
	}{
		// TODO: Add test cases.
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := run(); got != tt.want {
				t.Errorf("run() = %v, want %v", got, tt.want)
			}
		})
	}
}

func Test_main(t *testing.T) {
	tests := []struct {
		name string
	}{
		// TODO: Add test cases.
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			main()
		})
	}
}
