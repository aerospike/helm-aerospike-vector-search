package main

import (
	"bytes"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
)

// Add at the top of the file, after imports
var testConfigPath string
var configFilePath = AVS_CONFIG_FILE_PATH

// Add a mock REST config for testing
func getMockConfig() (*rest.Config, error) {
	// Create a fake client config that will work with our mock node/service
	return &rest.Config{
		Host: "fake",
		// Add transport wrapper to return our mock objects
		WrapTransport: func(rt http.RoundTripper) http.RoundTripper {
			return &mockTransport{
				node:    instance.node,
				service: instance.service,
			}
		},
	}, nil
}

// Add mock function
func mockK8sClient() kubernetes.Interface {
	return fake.NewSimpleClientset()
}

// Add mock transport
type mockTransport struct {
	node    *v1.Node
	service *v1.Service
}

func (t *mockTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	var obj runtime.Object

	// Return appropriate mock data based on the request path
	if strings.Contains(req.URL.Path, "/nodes/") {
		obj = t.node
	} else if strings.Contains(req.URL.Path, "/services/") {
		obj = t.service
	}

	// Encode as Kubernetes API response
	codec := scheme.Codecs.LegacyCodec(v1.SchemeGroupVersion)
	data, err := runtime.Encode(codec, obj)
	if err != nil {
		return nil, err
	}

	return &http.Response{
		StatusCode: http.StatusOK,
		Header: http.Header{
			"Content-Type": []string{"application/json"},
		},
		Body: io.NopCloser(bytes.NewReader(data)),
	}, nil
}

// Helper function to set up test environment
func setupTestEnv(t *testing.T) (string, func()) {
	tmpDir, err := os.MkdirTemp("", "avs-test-*")
	if err != nil {
		t.Fatal(err)
	}

	// Create config directory with proper permissions
	configDir := filepath.Join(tmpDir, "etc", "aerospike-vector-search")
	err = os.MkdirAll(configDir, 0777)
	if err != nil {
		t.Fatal(err)
	}

	testConfigPath = filepath.Join(configDir, "aerospike-vector-search.yml")
	// Create file with proper permissions
	f, err := os.OpenFile(testConfigPath, os.O_CREATE|os.O_WRONLY, 0666)
	if err != nil {
		t.Fatal(err)
	}
	f.Close()

	configFilePath = testConfigPath

	// Set required environment variables
	os.Setenv("NODE_NAME", "test-node")
	os.Setenv("POD_NAME", "avs-0")
	os.Setenv("POD_NAMESPACE", "test-ns")
	os.Setenv("SERVICE_NAME", "avs-service")
	os.Setenv("REPLICAS", "3")
	os.Setenv("AEROSPIKE_VECTOR_SEARCH_CONFIG", `{
		"service": {"ports": {"5000": {}}},
		"heartbeat": {"seeds": [{"address": "avs-0.test", "port": "5001"}]},
		"cluster": {}
	}`)
	os.Setenv("AEROSPIKE_VECTOR_SEARCH_NODE_ROLES", `{"test-label": ["role1", "role2"]}`)

	// Mock k8s client and assign to singleton
	instance = &NodeInfoSingleton{
		node: &v1.Node{
			ObjectMeta: metav1.ObjectMeta{
				Name: "test-node",
				Labels: map[string]string{
					AVS_NODE_LABEL_KEY: "test-label",
				},
			},
			Status: v1.NodeStatus{
				Addresses: []v1.NodeAddress{
					{Type: v1.NodeExternalIP, Address: "192.168.1.1"},
					{Type: v1.NodeInternalIP, Address: "10.0.0.1"},
				},
			},
		},
		service: &v1.Service{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "avs-service",
				Namespace: "test-ns",
			},
			Spec: v1.ServiceSpec{
				Type: v1.ServiceTypeNodePort,
				Ports: []v1.ServicePort{
					{NodePort: 30000},
				},
			},
		},
	}

	// Override config function for testing
	getConfig = getMockConfig

	cleanup := func() {
		os.RemoveAll(tmpDir)
		os.Clearenv()
		getConfig = rest.InClusterConfig // Restore default
	}

	return tmpDir, cleanup
}

func Test_getEndpointByMode(t *testing.T) {
	_, cleanup := setupTestEnv(t)
	defer cleanup()

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
	_, cleanup := setupTestEnv(t)
	defer cleanup()

	tests := []struct {
		name        string
		config      map[string]interface{}
		networkMode string
		wantErr     bool
	}{
		{
			name: "Valid NodePort configuration",
			config: map[string]interface{}{
				"service": map[string]interface{}{
					"ports": map[string]interface{}{
						"5000": map[string]interface{}{},
					},
				},
			},
			networkMode: "nodeport",
			wantErr:     false,
		},
		{
			name: "Valid HostNetwork configuration",
			config: map[string]interface{}{
				"service": map[string]interface{}{
					"ports": map[string]interface{}{
						"5000": map[string]interface{}{},
					},
				},
			},
			networkMode: "hostnetwork",
			wantErr:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			os.Setenv("NETWORK_MODE", tt.networkMode)
			os.Setenv("CONTAINER_PORT", "5000")
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
	_, cleanup := setupTestEnv(t)
	defer cleanup()

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
						"5000": map[string]interface{}{},
					},
				},
			},
			wantErr: false,
		},
		{
			name:    "Empty configuration",
			config:  map[string]interface{}{},
			wantErr: false,
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
		roles   string
		wantErr bool
	}{
		{
			name: "Valid configuration",
			config: `{
				"service": {"ports": {"5000": {}}},
				"heartbeat": {"seeds": [{"address": "avs-0.test", "port": "5001"}]},
				"cluster": {}
			}`,
			roles:   `{"test-label":["role1","role2"]}`,
			wantErr: false,
		},
		{
			name:    "Invalid configuration",
			config:  `invalid-json`,
			roles:   `{"test-label":["role1"]}`,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create fresh test environment for each test
			_, cleanup := setupTestEnv(t)
			defer cleanup()

			os.Setenv("POD_NAME", "avs-0")
			os.Setenv("REPLICAS", "3")
			os.Setenv("AEROSPIKE_VECTOR_SEARCH_CONFIG", tt.config)
			os.Setenv("AEROSPIKE_VECTOR_SEARCH_NODE_ROLES", tt.roles)
			os.Setenv("NETWORK_MODE", "nodeport")

			if got := run(); (got != 0) != tt.wantErr {
				t.Errorf("run() = %v, wantErr %v", got, tt.wantErr)
			}
		})
	}
}

func Test_generateHeartbeatSeedsDnsNames(t *testing.T) {
	tests := []struct {
		name      string
		config    map[string]interface{}
		podEnv    map[string]string
		wantSeeds []map[string]string
		wantErr   bool
	}{
		{
			name: "3 node cluster, middle pod",
			config: map[string]interface{}{
				"heartbeat": map[string]interface{}{
					"seeds": []interface{}{
						map[string]interface{}{
							"address": "avs-0.avs-internal.avs.svc.cluster.local",
							"port":    5001,
						},
					},
				},
			},
			podEnv: map[string]string{
				"POD_NAME": "avs-1",
				"REPLICAS": "3",
			},
			wantSeeds: []map[string]string{
				{
					"address": "avs-0.avs-internal.avs.svc.cluster.local",
					"port":    "5001",
				},
				{
					"address": "avs-2.avs-internal.avs.svc.cluster.local",
					"port":    "5001",
				},
			},
			wantErr: false,
		},
		{
			name: "Single node cluster",
			config: map[string]interface{}{
				"heartbeat": map[string]interface{}{
					"seeds": []interface{}{
						map[string]interface{}{
							"address": "avs-0.avs-internal.avs.svc.cluster.local",
							"port":    5001,
						},
					},
				},
			},
			podEnv: map[string]string{
				"POD_NAME": "avs-0",
				"REPLICAS": "1",
			},
			wantSeeds: []map[string]string{},
			wantErr:   false,
		},
		{
			name: "Missing REPLICAS env",
			config: map[string]interface{}{
				"heartbeat": map[string]interface{}{
					"seeds": []interface{}{
						map[string]interface{}{
							"address": "avs-0.avs-internal.avs.svc.cluster.local",
							"port":    5001,
						},
					},
				},
			},
			podEnv: map[string]string{
				"POD_NAME": "avs-0",
			},
			wantSeeds: nil,
			wantErr:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			os.Clearenv()
			for k, v := range tt.podEnv {
				os.Setenv(k, v)
			}

			got, err := generateHeartbeatSeedsDnsNames(tt.config)
			if (err != nil) != tt.wantErr {
				t.Errorf("generateHeartbeatSeedsDnsNames() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !reflect.DeepEqual(got, tt.wantSeeds) {
				t.Errorf("generateHeartbeatSeedsDnsNames() = %v, want %v", got, tt.wantSeeds)
			}
		})
	}
}

func Test_getDnsNameFormat(t *testing.T) {
	tests := []struct {
		name        string
		dnsName     string
		wantPodID   string
		wantPodName string
		wantErr     bool
	}{
		{
			name:        "Full kubernetes DNS name",
			dnsName:     "avs-0.avs-internal.avs.svc.cluster.local",
			wantPodID:   "0",
			wantPodName: "avs-0.avs-internal.avs.svc.cluster.local",
			wantErr:     false,
		},
		{
			name:        "Simple service name",
			dnsName:     "avs-0.avs-service",
			wantPodID:   "0",
			wantPodName: "avs-0.avs-service",
			wantErr:     false,
		},
		{
			name:        "Invalid format - no hyphen",
			dnsName:     "noformat.test",
			wantPodID:   "",
			wantPodName: "",
			wantErr:     true,
		},
		{
			name:        "Invalid format - no number",
			dnsName:     "avs-abc.test",
			wantPodID:   "",
			wantPodName: "",
			wantErr:     true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			podID, podName, err := getDnsNameFormat(tt.dnsName)
			if (err != nil) != tt.wantErr {
				t.Errorf("getDnsNameFormat() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if podID != tt.wantPodID {
				t.Errorf("getDnsNameFormat() podID = %v, want %v", podID, tt.wantPodID)
			}
			if podName != tt.wantPodName {
				t.Errorf("getDnsNameFormat() podName = %v, want %v", podName, tt.wantPodName)
			}
		})
	}
}

func Test_getHeartbeatSeeds(t *testing.T) {
	tests := []struct {
		name    string
		config  map[string]interface{}
		want    map[string]string
		wantErr bool
	}{
		{
			name: "Valid heartbeat config",
			config: map[string]interface{}{
				"heartbeat": map[string]interface{}{
					"seeds": []interface{}{
						map[string]interface{}{
							"address": "avs-0.avs-service",
							"port":    5001,
						},
					},
				},
			},
			want: map[string]string{
				"address": "avs-0.avs-service",
				"port":    "5001",
			},
			wantErr: false,
		},
		{
			name: "Missing heartbeat section",
			config: map[string]interface{}{
				"service": map[string]interface{}{},
			},
			want:    nil,
			wantErr: true,
		},
		{
			name: "Empty seeds list",
			config: map[string]interface{}{
				"heartbeat": map[string]interface{}{
					"seeds": []interface{}{},
				},
			},
			want:    nil,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := getHeartbeatSeeds(tt.config)
			if (err != nil) != tt.wantErr {
				t.Errorf("getHeartbeatSeeds() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("getHeartbeatSeeds() = %v, want %v", got, tt.want)
			}
		})
	}
}
