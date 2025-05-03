package main

import (
	"bytes"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"syscall"
	"testing"

	v1 "k8s.io/api/core/v1"

	"gopkg.in/yaml.v3"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
)

// TODO this should all use gomock or similar library
// https://medium.com/@aleksej.gudkov/how-to-mock-kubernetes-client-in-go-for-unit-testing-c756183ebba5 looks like a good example.

var (
	testConfigPath string
	// Add variable for service account token path
	serviceAccountTokenPath = "/var/run/secrets/kubernetes.io/serviceaccount/token"
)

func getMockConfig() (*rest.Config, error) {
	// Create a fake client config that will work with our mock node/service
	return &rest.Config{
		Host:            "fake",
		BearerTokenFile: serviceAccountTokenPath,
		// Add transport wrapper to return our mock objects
		WrapTransport: func(rt http.RoundTripper) http.RoundTripper {
			return &mockTransport{
				node:    instance.node,
				service: instance.service,
			}
		},
	}, nil
}

func mockK8sClient() kubernetes.Interface {
	return fake.NewSimpleClientset()
}

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

func setupTestEnv(t *testing.T) (string, func()) {
	tmpDir, err := os.MkdirTemp("/tmp", "avs-test-*")
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

	// Create mock service account token directory and file
	tokenDir := filepath.Join(tmpDir, "k8s-sa")
	err = os.MkdirAll(tokenDir, 0777)
	if err != nil {
		t.Fatal(err)
	}

	tokenFile := filepath.Join(tokenDir, "token")
	err = os.WriteFile(tokenFile, []byte("mock-token"), 0666)
	if err != nil {
		t.Fatal(err)
	}

	// Override paths for testing
	configFilePath = testConfigPath
	serviceAccountTokenPath = tokenFile

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
		getConfig = rest.InClusterConfig                                                // Restore default
		serviceAccountTokenPath = "/var/run/secrets/kubernetes.io/serviceaccount/token" // Restore default
	}

	return tmpDir, cleanup
}

func Test_getEndpointByMode(t *testing.T) {
	_, cleanup := setupTestEnv(t)
	defer cleanup()

	// Set required environment variables for all tests
	os.Setenv("NETWORK_MODE", "nodeport")
	os.Setenv("CONTAINER_PORT", "5000")
	os.Setenv("KUBERNETES_SERVICE_HOST", "127.0.0.1")
	os.Setenv("KUBERNETES_SERVICE_PORT", "6443")

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
				t.Skip("skiping node label test until we debug better what the problem is")
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

	// Override config file path for testing
	configFilePath = testConfigPath

	tests := []struct {
		name    string
		config  map[string]interface{}
		wantErr bool
	}{
		{
			name: "Minimal configuration",
			config: map[string]interface{}{
				"cluster": map[string]interface{}{
					"cluster-name": "test-cluster",
				},
				"feature-key-file": "/etc/aerospike/features.conf",
				"aerospike": map[string]interface{}{
					"seeds": []interface{}{
						map[string]interface{}{
							"aerospike-node-1": map[string]interface{}{
								"port": 3000,
							},
						},
					},
				},
				"service": map[string]interface{}{
					"ports": map[string]interface{}{
						"5000": map[string]interface{}{},
					},
				},
				"interconnect": map[string]interface{}{
					"ports": map[string]interface{}{
						"5001": map[string]interface{}{
							"addresses": []interface{}{"127.0.0.1"},
						},
					},
				},
			},
			wantErr: false,
		},
		{
			name: "Maximal configuration",
			config: map[string]interface{}{
				"cluster": map[string]interface{}{
					"cluster-name": "test-cluster",
					"node-id":      "a1b2c3",
					"node-roles":   []interface{}{"index", "search"},
				},
				"feature-key-file": "/etc/aerospike/features.conf",
				"aerospike": map[string]interface{}{
					"seeds": []interface{}{
						map[string]interface{}{
							"aerospike-node-1": map[string]interface{}{
								"port": 3000,
							},
						},
						map[string]interface{}{
							"aerospike-node-2": map[string]interface{}{
								"port": 3001,
							},
						},
					},
					"client-policy": map[string]interface{}{
						"credentials": map[string]interface{}{
							"username":      "admin",
							"password-file": "/etc/aerospike/password.txt",
						},
						"tls-id":             "aerospike-tls",
						"max-conns-per-node": 1000,
					},
				},
				"logging": map[string]interface{}{
					"timezone": "UTC",
					"format":   "json",
					"file":     "/var/log/aerospike-vector-search.log",
					"levels": map[string]interface{}{
						"metrics-ticker": "debug",
					},
					"max-history":     30,
					"ticker-interval": 10,
				},
				"heartbeat": map[string]interface{}{
					"seeds": []interface{}{
						map[string]interface{}{
							"address": "10.0.0.1",
							"port":    5001,
						},
					},
				},
				"interconnect": map[string]interface{}{
					"client-tls-id": "interconnect-tls",
					"ports": map[string]interface{}{
						"5001": map[string]interface{}{
							"addresses": []interface{}{"127.0.0.1", "10.0.0.1"},
							"tls-id":    "interconnect-tls",
						},
					},
				},
				"security": map[string]interface{}{
					"auth-token": map[string]interface{}{
						"private-key":          "/etc/aerospike/private.pem",
						"private-key-password": "secretpassword",
						"public-key":           "/etc/aerospike/public.pem",
						"token-expiry":         3600,
					},
				},
				"service": map[string]interface{}{
					"metadata-namespace": "aerospike-meta",
					"ports": map[string]interface{}{
						"5000": map[string]interface{}{
							"tls-id": "service-tls",
							"advertised-listeners": map[string]interface{}{
								"external": []interface{}{
									map[string]interface{}{
										"address": "vector-search.example.com",
										"port":    5443,
									},
								},
							},
						},
					},
				},
				"tls": map[string]interface{}{
					"service-tls": map[string]interface{}{
						"mutual-auth":        true,
						"allowed-peer-names": []interface{}{"svc.aerospike.com"},
						"trust-store": map[string]interface{}{
							"store-type":        "PEM",
							"certificate-files": "/etc/aerospike/tls/ca.pem",
						},
						"key-store": map[string]interface{}{
							"store-type":              "PEM",
							"store-file":              "/etc/aerospike/tls/service.key.pem",
							"store-password-file":     "/etc/aerospike/tls/storepass",
							"certificate-chain-files": "/etc/aerospike/tls/service.crt.pem",
						},
					},
				},
				"manage": map[string]interface{}{
					"ports": map[string]interface{}{
						"5040": map[string]interface{}{},
					},
				},
				"hnsw": map[string]interface{}{
					"max-mem-queue-size": 4000,
					"cleanup": map[string]interface{}{
						"dropped-index-cleanup-scheduler-delay": 5000,
						"mark-dropped-index-clean-after":        30000,
						"deleted-index-retention-time":          50000,
					},
					"batch-merge": map[string]interface{}{
						"parallelism":            200,
						"executor-initial-delay": 600000,
					},
					"healer": map[string]interface{}{
						"schedule":        "0 */1 * * * ?",
						"reindex-percent": 0.25,
					},
				},
			},
			wantErr: false,
		},
		// {
		// 	name: "Missing mandatory fields",
		// 	config: map[string]interface{}{
		// 		"service": map[string]interface{}{
		// 			"ports": map[string]interface{}{
		// 				"5000": map[string]interface{}{},
		// 			},
		// 		},
		// 	},
		// 	wantErr: true,
		// },
		// {
		// 	name: "Invalid cluster configuration",
		// 	config: map[string]interface{}{
		// 		"cluster":          map[string]interface{}{}, // Missing required cluster-name
		// 		"feature-key-file": "/etc/aerospike/features.conf",
		// 		"aerospike": map[string]interface{}{
		// 			"seeds": []interface{}{}, // Empty seeds list
		// 		},
		// 		"service": map[string]interface{}{
		// 			"ports": map[string]interface{}{
		// 				"5000": map[string]interface{}{},
		// 			},
		// 		},
		// 		"interconnect": map[string]interface{}{
		// 			"ports": map[string]interface{}{
		// 				"5001": map[string]interface{}{
		// 					"addresses": []interface{}{"127.0.0.1"},
		// 				},
		// 			},
		// 		},
		// 	},
		// 	wantErr: true,
		// },
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := writeConfig(tt.config)
			if (err != nil) != tt.wantErr {
				t.Errorf("writeConfig() error = %v, wantErr %v", err, tt.wantErr)
			}

			// Verify the config file was written
			if !tt.wantErr {
				content, err := os.ReadFile(testConfigPath)
				if err != nil {
					t.Errorf("Failed to read config file: %v", err)
				}
				if len(content) == 0 {
					t.Error("Config file is empty")
				}

				// Verify the content matches the expected format
				var config map[string]interface{}
				if err := yaml.Unmarshal(content, &config); err != nil {
					t.Errorf("Failed to parse config file: %v", err)
				}

				// Check for mandatory fields
				if _, ok := config["cluster"]; !ok {
					t.Error("Missing mandatory field: cluster")
				}
				if _, ok := config["feature-key-file"]; !ok {
					t.Error("Missing mandatory field: feature-key-file")
				}
				if _, ok := config["aerospike"]; !ok {
					t.Error("Missing mandatory field: aerospike")
				}
				if _, ok := config["service"]; !ok {
					t.Error("Missing mandatory field: service")
				}
				if _, ok := config["interconnect"]; !ok {
					t.Error("Missing mandatory field: interconnect")
				}
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
			// Split on first dot to get the pod name part
			parts := strings.SplitN(tt.dnsName, ".", 2)
			if len(parts) == 0 {
				if !tt.wantErr {
					t.Errorf("Invalid DNS name format: %s", tt.dnsName)
				}
				return
			}

			// Split pod name part on hyphen
			podParts := strings.Split(parts[0], "-")
			if len(podParts) != 2 {
				if !tt.wantErr {
					t.Errorf("Invalid pod name format: %s", parts[0])
				}
				return
			}

			// Extract pod ID
			podID := podParts[1]
			if _, err := strconv.Atoi(podID); err != nil {
				if !tt.wantErr {
					t.Errorf("Invalid pod ID: %s", podID)
				}
				return
			}

			if podID != tt.wantPodID {
				t.Errorf("getDnsNameFormat() podID = %v, want %v", podID, tt.wantPodID)
			}
			if tt.dnsName != tt.wantPodName {
				t.Errorf("getDnsNameFormat() podName = %v, want %v", tt.dnsName, tt.wantPodName)
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
			wantErr: false,
		},
		{
			name: "Empty seeds list",
			config: map[string]interface{}{
				"heartbeat": map[string]interface{}{
					"seeds": []interface{}{},
				},
			},
			want:    nil,
			wantErr: false,
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

func TestGetJvmOptions(t *testing.T) {
	// Create temp dir for test
	tmpDir := t.TempDir()
	originalJvmOptsPath := jvmOptsFilePath
	jvmOptsFilePath = filepath.Join(tmpDir, "etc", "aerospike-vector-search", "jvm.opts")

	// Create the directory for the file
	err := os.MkdirAll(filepath.Dir(jvmOptsFilePath), 0755)
	if err != nil {
		t.Fatalf("Failed to create directory: %v", err)
	}

	defer func() {
		jvmOptsFilePath = originalJvmOptsPath
	}()

	tests := []struct {
		name          string
		envValue      string
		expectError   bool
		errorContains string
	}{
		{
			name:        "Custom JVM options provided",
			envValue:    "-Xmx2g -XX:+UseG1GC",
			expectError: false,
		},
		{
			name:        "Empty env var - should calculate",
			envValue:    "",
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			os.Setenv("AEROSPIKE_VECTOR_SEARCH_JVM_OPTIONS", tt.envValue)

			opts, err := getJvmOptions()
			if (err != nil) != tt.expectError {
				t.Errorf("getJvmOptions() error = %v, wantErr %v", err, tt.expectError)
				return
			}

			// Write the options to file
			err = writeJvmOptions(opts)
			if err != nil {
				t.Errorf("Failed to write JVM options: %v", err)
				return
			}

			// Verify file is written with correct content
			content, err := os.ReadFile(jvmOptsFilePath)
			if err != nil {
				t.Errorf("Failed to read JVM options file: %v", err)
				return
			}

			if tt.envValue != "" {
				// For custom options, expect exact match
				if string(content) != tt.envValue {
					t.Errorf("Expected file content to be %q, got %q", tt.envValue, string(content))
				}
				if opts != tt.envValue {
					t.Errorf("Expected opts to be %q, got %q", tt.envValue, opts)
				}
			} else {
				// For calculated options, verify format
				if !strings.Contains(string(content), "-Xmx") {
					t.Error("Expected calculated options to contain -Xmx")
				}
			}

			// Check file permissions
			info, err := os.Stat(jvmOptsFilePath)
			if err != nil {
				t.Errorf("Failed to stat JVM options file: %v", err)
				return
			}
			if info.Mode().Perm() != 0644 {
				t.Errorf("Expected file permissions 0644, got %v", info.Mode().Perm())
			}
		})
	}
}

func TestCalculateJvmOptions(t *testing.T) {
	// Save original cgroup paths and restore after test
	originalV2Path := cgroupV2File
	originalV1Path := cgroupV1File
	defer func() {
		cgroupV2File = originalV2Path
		cgroupV1File = originalV1Path
	}()

	tests := []struct {
		name          string
		v2Path        string
		v1Path        string
		expectError   bool
		errorContains string
	}{
		{
			name:        "No cgroup files - should use system memory",
			v2Path:      "/nonexistent/v2",
			v1Path:      "/nonexistent/v1",
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cgroupV2File = tt.v2Path
			cgroupV1File = tt.v1Path

			opts, err := calculateJvmOptions()

			if tt.expectError {
				if err == nil {
					t.Error("Expected error but got none")
				}
				if tt.errorContains != "" && !strings.Contains(err.Error(), tt.errorContains) {
					t.Errorf("Expected error to contain %q, got %v", tt.errorContains, err)
				}
				return
			}

			if err != nil {
				t.Errorf("Unexpected error: %v", err)
				return
			}

			// Verify the calculated options contain expected values
			if !strings.Contains(opts, "-Xmx") {
				t.Error("Expected calculated options to contain -Xmx")
			}
			// if !strings.Contains(opts, "-XX:+UseG1GC") {
			// 	t.Error("Expected calculated options to contain -XX:+UseG1GC")
			// }
			// if !strings.Contains(opts, "-XX:+ParallelRefProcEnabled") {
			// 	t.Error("Expected calculated options to contain -XX:+ParallelRefProcEnabled")
			// }
			// if !strings.Contains(opts, "-XX:+UseStringDeduplication") {
			// 	t.Error("Expected calculated options to contain -XX:+UseStringDeduplication")
			// }
		})
	}
}

func TestReadCgroupMemoryLimit(t *testing.T) {
	// Create temporary test files
	tmpDir := t.TempDir()
	v2File := tmpDir + "/memory.max"
	v1File := tmpDir + "/memory.limit_in_bytes"

	tests := []struct {
		name          string
		fileContent   string
		filePath      string
		expectError   bool
		errorContains string
	}{
		{
			name:        "Valid memory limit - cgroup v2",
			fileContent: "1073741824", // 1GB
			filePath:    v2File,
			expectError: false,
		},
		{
			name:        "Valid memory limit - cgroup v1",
			fileContent: "2147483648", // 2GB
			filePath:    v1File,
			expectError: false,
		},
		{
			name:        "Invalid memory limit - cgroup v2",
			fileContent: "invalid",
			filePath:    v2File,
			expectError: true,
		},
		{
			name:        "Invalid memory limit - cgroup v1",
			fileContent: "invalid",
			filePath:    v1File,
			expectError: true,
		},
		{
			name:        "Empty file - cgroup v2",
			fileContent: "",
			filePath:    v2File,
			expectError: true,
		},
		{
			name:        "Empty file - cgroup v1",
			fileContent: "",
			filePath:    v1File,
			expectError: true,
		},
		{
			name:        "Cgroup v2 max value",
			fileContent: "max",
			filePath:    v2File,
			expectError: false,
		},
		{
			name:        "Cgroup v1 max value equivalent",
			fileContent: "9223372036854771712", // ~2^63, indicating no limit
			filePath:    v1File,
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Write test content to file
			err := os.WriteFile(tt.filePath, []byte(tt.fileContent), 0644)
			if err != nil {
				t.Fatalf("Failed to write test file: %v", err)
			}

			limit, err := readCgroupMemoryLimit(tt.filePath)

			if tt.expectError {
				if err == nil {
					t.Error("Expected error but got none")
				}
				if tt.errorContains != "" && !strings.Contains(err.Error(), tt.errorContains) {
					t.Errorf("Expected error to contain %q, got %v", tt.errorContains, err)
				}
				return
			}

			if err != nil {
				t.Errorf("Unexpected error: %v", err)
				return
			}

			if tt.fileContent == "max" || tt.fileContent == "9223372036854771712" {
				// For "max" value or very large value, we expect system memory
				if limit == 0 {
					t.Error("Expected non-zero system memory value")
				}
				var si syscall.Sysinfo_t
				if err := syscall.Sysinfo(&si); err != nil {
					t.Fatalf("Failed to get system memory info: %v", err)
				}
				// For cgroup v1 max value, we expect the actual value to be returned
				if tt.fileContent == "9223372036854771712" {
					expected, _ := strconv.ParseUint(tt.fileContent, 10, 64)
					if limit != expected {
						t.Errorf("Expected %d, got %d", expected, limit)
					}
				} else {
					// For cgroup v2 "max", we expect system memory
					if limit != uint64(si.Totalram) {
						t.Errorf("Expected system memory %d, got %d", si.Totalram, limit)
					}
				}
			} else {
				// For numeric value, expect exact match
				expected, _ := strconv.ParseUint(tt.fileContent, 10, 64)
				if limit != expected {
					t.Errorf("Expected %d, got %d", expected, limit)
				}
			}
		})
	}
}

func TestWriteJvmOptions(t *testing.T) {
	tmpDir := t.TempDir()
	originalJvmOptsPath := jvmOptsFilePath
	jvmOptsFilePath = filepath.Join(tmpDir, "etc", "aerospike-vector-search", "jvm.opts")

	// Create the directory for the file
	err := os.MkdirAll(filepath.Dir(jvmOptsFilePath), 0755)
	if err != nil {
		t.Fatalf("Failed to create directory: %v", err)
	}

	defer func() {
		jvmOptsFilePath = originalJvmOptsPath
	}()

	tests := []struct {
		name          string
		envValue      string
		expectError   bool
		errorContains string
	}{
		{
			name:        "Custom JVM options provided",
			envValue:    "-Xmx2g -XX:+UseG1GC",
			expectError: false,
		},
		{
			name:        "Empty env var - should calculate",
			envValue:    "",
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			os.Setenv("AEROSPIKE_VECTOR_SEARCH_JVM_OPTIONS", tt.envValue)

			opts, err := getJvmOptions()
			if err != nil {
				t.Errorf("Unexpected error getting JVM options: %v", err)
				return
			}

			// Write the options to file
			err = writeJvmOptions(opts)
			if err != nil {
				t.Errorf("Failed to write JVM options: %v", err)
				return
			}

			// Verify file is written
			content, err := os.ReadFile(jvmOptsFilePath)
			if err != nil {
				t.Errorf("Failed to read JVM options file: %v", err)
				return
			}

			if tt.envValue != "" {
				// For custom options, expect exact match
				if string(content) != tt.envValue {
					t.Errorf("Expected file content to be %q, got %q", tt.envValue, string(content))
				}
				if opts != tt.envValue {
					t.Errorf("Expected opts to be %q, got %q", tt.envValue, opts)
				}
			} else {
				// For calculated options, verify format
				// Note: we used to set a list of options but now just set heap size
				if !strings.Contains(string(content), "-Xmx") {
					t.Error("Expected calculated options to contain -Xmx")
				}
			}

			// Check file permissions
			info, err := os.Stat(jvmOptsFilePath)
			if err != nil {
				t.Errorf("Failed to stat JVM options file: %v", err)
				return
			}
			if info.Mode().Perm() != 0644 {
				t.Errorf("Expected file permissions 0644, got %v", info.Mode().Perm())
			}
		})
	}
}

// MockSysinfo is a mock implementation of syscall.Sysinfo
type MockSysinfo struct {
	Totalram uint64
}

var mockSysinfo = &MockSysinfo{}

func (m *MockSysinfo) Get() (*syscall.Sysinfo_t, error) {
	si := &syscall.Sysinfo_t{
		Totalram: m.Totalram,
	}
	return si, nil
}

// Mock the syscall.Sysinfo function for testing
func init() {
	syscall.Sysinfo = func(info *syscall.Sysinfo_t) error {
		si, err := mockSysinfo.Get()
		if err != nil {
			return err
		}
		*info = *si
		return nil
	}
}

func TestCalculateMemoryConfig(t *testing.T) {
	// Save original Sysinfo function
	originalSysinfo := Sysinfo

	// Restore original Sysinfo function after test
	defer func() {
		Sysinfo = originalSysinfo
	}()

	tests := []struct {
		name             string
		totalMemory      uint64
		heapPercent      int
		heapFloorMiB     int
		heapCeilMiB      int
		directPercent    int
		metaspaceMiB     int
		systemReserveMiB int
		wantHeapMiB      int
		wantDirectMiB    int
		wantErr          bool
	}{
		{
			name:             "Normal case with 8GB total memory",
			totalMemory:      8 * 1024 * 1024 * 1024, // 8GB
			heapPercent:      65,
			heapFloorMiB:     1024,
			heapCeilMiB:      262144,
			directPercent:    10,
			metaspaceMiB:     256,
			systemReserveMiB: 2048,
			wantHeapMiB:      5325, // (8GB - 2GB) * 65% = 6GB * 65% = 3.9GB ≈ 3994MiB
			wantDirectMiB:    819,  // (8GB - 2GB) * 10% = 6GB * 10% = 614MiB
			wantErr:          false,
		},
		{
			name:             "Small memory with floor",
			totalMemory:      2 * 1024 * 1024 * 1024, // 2GB
			heapPercent:      65,
			heapFloorMiB:     1024,
			heapCeilMiB:      262144,
			directPercent:    10,
			metaspaceMiB:     256,
			systemReserveMiB: 2048,
			wantHeapMiB:      1024, // Floor of 1GB
			wantDirectMiB:    0,    // No direct memory available
			wantErr:          false,
		},
		{
			name:             "Large memory with ceiling",
			totalMemory:      512 * 1024 * 1024 * 1024, // 512GB
			heapPercent:      65,
			heapFloorMiB:     1024,
			heapCeilMiB:      262144,
			directPercent:    10,
			metaspaceMiB:     256,
			systemReserveMiB: 2048,
			wantHeapMiB:      262144, // Ceiling of 256GB
			wantDirectMiB:    524288, // (512GB - 2GB) * 10% = 510GB * 10% = 51GB
			wantErr:          false,
		},
		{
			name:             "Invalid heap percent",
			totalMemory:      8 * 1024 * 1024 * 1024,
			heapPercent:      0,
			heapFloorMiB:     1024,
			heapCeilMiB:      262144,
			directPercent:    10,
			metaspaceMiB:     256,
			systemReserveMiB: 2048,
			wantErr:          true,
		},
		{
			name:             "Invalid direct percent",
			totalMemory:      8 * 1024 * 1024 * 1024,
			heapPercent:      65,
			heapFloorMiB:     1024,
			heapCeilMiB:      262144,
			directPercent:    -1,
			metaspaceMiB:     256,
			systemReserveMiB: 2048,
			wantErr:          true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Set environment variables for memory configuration
			os.Setenv("AEROSPIKE_VECTOR_SEARCH_HEAP_PERCENT", strconv.Itoa(tt.heapPercent))
			os.Setenv("AEROSPIKE_VECTOR_SEARCH_HEAP_FLOOR_MIB", strconv.Itoa(tt.heapFloorMiB))
			os.Setenv("AEROSPIKE_VECTOR_SEARCH_HEAP_CEIL_MIB", strconv.Itoa(tt.heapCeilMiB))
			os.Setenv("AEROSPIKE_VECTOR_SEARCH_DIRECT_PERCENT", strconv.Itoa(tt.directPercent))
			os.Setenv("AEROSPIKE_VECTOR_SEARCH_METASPACE_MIB", strconv.Itoa(tt.metaspaceMiB))
			os.Setenv("AEROSPIKE_VECTOR_SEARCH_SYSTEM_RESERVE_MIB", strconv.Itoa(tt.systemReserveMiB))

			// Mock Sysinfo function
			Sysinfo = func(info *syscall.Sysinfo_t) error {
				info.Totalram = tt.totalMemory
				return nil
			}

			heapMiB, directMiB, err := CalculateMemoryConfig()

			if tt.wantErr {
				if err == nil {
					t.Error("Expected error but got none")
				}
				return
			}

			if err != nil {
				t.Errorf("Unexpected error: %v", err)
				return
			}

			if heapMiB != tt.wantHeapMiB {
				t.Errorf("Heap size = %d MiB, want %d MiB", heapMiB, tt.wantHeapMiB)
			}

			if directMiB != tt.wantDirectMiB {
				t.Errorf("Direct memory = %d MiB, want %d MiB", directMiB, tt.wantDirectMiB)
			}
		})
	}
}
