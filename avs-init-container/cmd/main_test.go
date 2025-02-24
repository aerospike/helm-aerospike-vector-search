package main

import (
	"os"
	"testing"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func Test_getEndpointByMode(t *testing.T) {
	tests := []struct {
		name          string
		networkMode   string
		containerPort string
		node          *v1.Node
		service       *v1.Service
		wantIP        string
		wantPort      int32
		wantErr       bool
	}{
		{
			name:        "NodePort mode with valid external IP and NodePort",
			networkMode: "nodeport",
			node: &v1.Node{
				Status: v1.NodeStatus{
					Addresses: []v1.NodeAddress{
						{Type: v1.NodeExternalIP, Address: "192.168.1.1"},
					},
				},
			},
			service: &v1.Service{
				Spec: v1.ServiceSpec{
					Type: v1.ServiceTypeNodePort,
					Ports: []v1.ServicePort{
						{NodePort: 30000},
					},
				},
			},
			wantIP:   "192.168.1.1",
			wantPort: 30000,
			wantErr:  false,
		},
		{
			name:        "NodePort mode with valid internal IP and NodePort",
			networkMode: "nodeport",
			node: &v1.Node{
				Status: v1.NodeStatus{
					Addresses: []v1.NodeAddress{
						{Type: v1.NodeInternalIP, Address: "10.0.0.1"},
					},
				},
			},
			service: &v1.Service{
				Spec: v1.ServiceSpec{
					Type: v1.ServiceTypeNodePort,
					Ports: []v1.ServicePort{
						{NodePort: 30000},
					},
				},
			},
			wantIP:   "10.0.0.1",
			wantPort: 30000,
			wantErr:  false,
		},
		{
			name:          "HostNetwork mode with valid container port",
			networkMode:   "hostnetwork",
			containerPort: "8080",
			node: &v1.Node{
				Status: v1.NodeStatus{
					Addresses: []v1.NodeAddress{
						{Type: v1.NodeInternalIP, Address: "10.0.0.1"},
					},
				},
			},
			wantIP:   "10.0.0.1",
			wantPort: 8080,
			wantErr:  false,
		},
		{
			name:          "HostNetwork mode with invalid container port",
			networkMode:   "hostnetwork",
			containerPort: "invalid",
			node: &v1.Node{
				Status: v1.NodeStatus{
					Addresses: []v1.NodeAddress{
						{Type: v1.NodeInternalIP, Address: "10.0.0.1"},
					},
				},
			},
			wantIP:   "",
			wantPort: 0,
			wantErr:  true,
		},
		{
			name:        "Unsupported network mode",
			networkMode: "unsupported",
			node: &v1.Node{
				Status: v1.NodeStatus{
					Addresses: []v1.NodeAddress{
						{Type: v1.NodeInternalIP, Address: "10.0.0.1"},
					},
				},
			},
			wantIP:   "",
			wantPort: 0,
			wantErr:  true,
		},
		{
			name:        "No valid node IP found",
			networkMode: "nodeport",
			node: &v1.Node{
				Status: v1.NodeStatus{
					Addresses: []v1.NodeAddress{},
				},
			},
			wantIP:   "",
			wantPort: 0,
			wantErr:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			os.Setenv("NETWORK_MODE", tt.networkMode)
			os.Setenv("CONTAINER_PORT", tt.containerPort)
			instance = &NodeInfoSingleton{node: tt.node, service: tt.service}

			gotIP, gotPort, err := getEndpointByMode()
			if (err != nil) != tt.wantErr {
				t.Errorf("getEndpointByMode() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if gotIP != tt.wantIP {
				t.Errorf("getEndpointByMode() gotIP = %v, want %v", gotIP, tt.wantIP)
			}
			if gotPort != tt.wantPort {
				t.Errorf("getEndpointByMode() gotPort = %v, want %v", gotPort, tt.wantPort)
			}
		})
	}
}

func Test_setAdvertisedListeners(t *testing.T) {
	tests := []struct {
		name    string
		config  map[string]interface{}
		wantErr bool
	}{
		{
			name: "Valid configuration",
			config: map[string]interface{}{
				"service": map[string]interface{}{
					"ports": map[string]interface{}{
						"http": map[string]interface{}{
							"port": 8080,
						},
					},
				},
			},
			wantErr: false,
		},
		{
			name:    "Invalid configuration",
			config:  map[string]interface{}{},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := setAdvertisedListeners(tt.config)
			if (err != nil) != tt.wantErr {
				t.Errorf("setAdvertisedListeners() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func Test_getNodeLabels(t *testing.T) {
	tests := []struct {
		name    string
		node    *v1.Node
		want    string
		wantErr bool
	}{
		{
			name: "Node with label",
			node: &v1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						AVS_NODE_LABEL_KEY: "test-label",
					},
				},
			},
			want:    "test-label",
			wantErr: false,
		},
		{
			name: "Node without label",
			node: &v1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{},
				},
			},
			want:    "",
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			instance = &NodeInfoSingleton{node: tt.node}
			got, err := getNodeLabels()
			if (err != nil) != tt.wantErr {
				t.Errorf("getNodeLabels() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if got != tt.want {
				t.Errorf("getNodeLabels() got = %v, want %v", got, tt.want)
			}
		})
	}
}

func Test_setRoles(t *testing.T) {
	tests := []struct {
		name    string
		config  map[string]interface{}
		roles   string
		wantErr bool
	}{
		{
			name: "Valid roles",
			config: map[string]interface{}{
				"cluster": map[string]interface{}{},
			},
			roles:   `{"test-label": ["role1", "role2"]}`,
			wantErr: false,
		},
		{
			name: "Invalid roles",
			config: map[string]interface{}{
				"cluster": map[string]interface{}{},
			},
			roles:   `invalid-json`,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			os.Setenv("AEROSPIKE_VECTOR_SEARCH_NODE_ROLES", tt.roles)
			instance = &NodeInfoSingleton{node: &v1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						AVS_NODE_LABEL_KEY: "test-label",
					},
				},
			}}
			err := setRoles(tt.config)
			if (err != nil) != tt.wantErr {
				t.Errorf("setRoles() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func Test_writeConfig(t *testing.T) {
	tests := []struct {
		name    string
		config  map[string]interface{}
		wantErr bool
	}{
		{
			name: "Valid configuration",
			config: map[string]interface{}{
				"service": map[string]interface{}{
					"ports": map[string]interface{}{
						"http": map[string]interface{}{
							"port": 8080,
						},
					},
				},
			},
			wantErr: false,
		},
		{
			name:    "Invalid configuration",
			config:  map[string]interface{}{},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := writeConfig(tt.config)
			if (err != nil) != tt.wantErr {
				t.Errorf("writeConfig() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func Test_run(t *testing.T) {
	tests := []struct {
		name    string
		config  string
		wantErr bool
	}{
		{
			name:    "Valid configuration",
			config:  `{"service": {"ports": {"http": {"port": 8080}}}}`,
			wantErr: false,
		},
		{
			name:    "Invalid configuration",
			config:  `invalid-json`,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			os.Setenv("AEROSPIKE_VECTOR_SEARCH_CONFIG", tt.config)
			if got := run(); (got != 0) != tt.wantErr {
				t.Errorf("run() = %v, wantErr %v", got, tt.wantErr)
			}
		})
	}
}
