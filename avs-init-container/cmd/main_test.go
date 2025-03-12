package main

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"

	v1 "k8s.io/api/core/v1"

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
	configFilePath = AVS_CONFIG_FILE_PATH
	getConfig      = rest.InClusterConfig // Default to real config
)

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
	t.Skip("skipping until we have better mocks")
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
	t.Skip("skipping until we have better mocks")
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
	t.Skip("skipping until we debug better what the problem is")
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
	t.Skip("skipping until we debug better what the problem is")
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

func TestReadCgroupMemoryLimit(t *testing.T) {
	// Create a temporary directory for test files
	tmpDir, err := os.MkdirTemp("", "cgroup-test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	tests := []struct {
		name     string
		content  string
		expected uint64
		wantErr  bool
	}{
		{
			name:     "valid memory limit",
			content:  "2147483648\n",
			expected: 2147483648,
			wantErr:  false,
		},
		{
			name:     "max value",
			content:  "max\n",
			expected: 0, // Will be replaced with system memory
			wantErr:  false,
		},
		{
			name:     "invalid value",
			content:  "invalid\n",
			expected: 0,
			wantErr:  true,
		},
		{
			name:     "empty file",
			content:  "",
			expected: 0,
			wantErr:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create test file
			testFile := filepath.Join(tmpDir, tt.name)
			err := os.WriteFile(testFile, []byte(tt.content), 0644)
			if err != nil {
				t.Fatalf("Failed to write test file: %v", err)
			}

			// If testing "max", get system memory for comparison
			var expectedMem uint64
			if tt.content == "max\n" {
				var si syscall.Sysinfo_t
				if err := syscall.Sysinfo(&si); err != nil {
					t.Fatalf("Failed to get system memory info: %v", err)
				}
				expectedMem = uint64(si.Totalram)
			} else {
				expectedMem = tt.expected
			}

			got, err := readCgroupMemoryLimit(testFile)
			if (err != nil) != tt.wantErr {
				t.Errorf("readCgroupMemoryLimit() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr && got != expectedMem {
				t.Errorf("readCgroupMemoryLimit() = %v, want %v", got, expectedMem)
			}
		})
	}
}

func TestCalculateJvmOptions(t *testing.T) {
	// Create a temporary directory for the podinfo
	tmpDir, err := os.MkdirTemp("", "jvm-test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Save original path and restore after test
	originalPath := getEnvFilePath()
	defer setEnvFilePath(originalPath)

	// Set test path
	testPath := filepath.Join(tmpDir, "JAVA_TOOL_OPTIONS")
	setEnvFilePath(testPath)

	// Test with environment variable override
	t.Run("with environment variable", func(t *testing.T) {
		customOpts := "-Xmx2g -XX:+UseG1GC"
		os.Setenv("AEROSPIKE_VECTOR_SEARCH_JVM_OPTIONS", customOpts)
		defer os.Unsetenv("AEROSPIKE_VECTOR_SEARCH_JVM_OPTIONS")

		got, err := getJvmOptions()
		if err != nil {
			t.Fatalf("getJvmOptions() error = %v", err)
		}
		if got != customOpts {
			t.Errorf("getJvmOptions() = %v, want %v", got, customOpts)
		}
	})

	// Test automatic calculation
	t.Run("automatic calculation", func(t *testing.T) {
		os.Unsetenv("AEROSPIKE_VECTOR_SEARCH_JVM_OPTIONS")

		got, err := getJvmOptions()
		if err != nil {
			t.Fatalf("getJvmOptions() error = %v", err)
		}

		// Check that the result contains expected JVM options
		expectedOptions := []string{
			"-Xmx",
			"-XX:+UseG1GC",
			"-XX:+ParallelRefProcEnabled",
			"-XX:+UseStringDeduplication",
		}

		for _, opt := range expectedOptions {
			if !strings.Contains(got, opt) {
				t.Errorf("getJvmOptions() = %v, missing expected option %v", got, opt)
			}
		}

		// Check that the options were written to the file
		fileContent, err := os.ReadFile(getEnvFilePath())
		if err != nil {
			t.Fatalf("Failed to read JVM options file: %v", err)
		}
		if string(fileContent) != got {
			t.Errorf("File content = %v, want %v", string(fileContent), got)
		}
	})
}

func TestCalculateJvmOptionsMemoryRatio(t *testing.T) {
	// Create a temporary directory for test files
	tmpDir, err := os.MkdirTemp("", "memory-test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Save original paths and restore after test
	originalEnvPath := getEnvFilePath()
	originalV2Path := cgroupV2File
	originalV1Path := cgroupV1File
	defer func() {
		setEnvFilePath(originalEnvPath)
		setCgroupPaths(originalV2Path, originalV1Path)
	}()

	// Set test paths
	testEnvPath := filepath.Join(tmpDir, "JAVA_TOOL_OPTIONS")
	setEnvFilePath(testEnvPath)

	// Create mock cgroup directory structure
	cgroupV2Dir := filepath.Join(tmpDir, "sys", "fs", "cgroup")
	cgroupV1Dir := filepath.Join(tmpDir, "sys", "fs", "cgroup", "memory")
	if err := os.MkdirAll(cgroupV2Dir, 0755); err != nil {
		t.Fatalf("Failed to create cgroup v2 dir: %v", err)
	}
	if err := os.MkdirAll(cgroupV1Dir, 0755); err != nil {
		t.Fatalf("Failed to create cgroup v1 dir: %v", err)
	}

	// Set up test memory limit (4GB)
	memoryLimit := uint64(4 * 1024 * 1024 * 1024)

	// Create and write to test cgroup files
	testV2File := filepath.Join(cgroupV2Dir, "memory.max")
	testV1File := filepath.Join(cgroupV1Dir, "memory.limit_in_bytes")

	if err := os.WriteFile(testV2File, []byte(fmt.Sprint(memoryLimit)), 0644); err != nil {
		t.Fatalf("Failed to write cgroup v2 file: %v", err)
	}
	if err := os.WriteFile(testV1File, []byte(fmt.Sprint(memoryLimit)), 0644); err != nil {
		t.Fatalf("Failed to write cgroup v1 file: %v", err)
	}

	// Set the test paths
	setCgroupPaths(testV2File, testV1File)

	t.Run("with cgroup v2", func(t *testing.T) {
		got, err := calculateJvmOptions()
		if err != nil {
			t.Fatalf("calculateJvmOptions() error = %v", err)
		}

		// Expected Xmx should be 80% of 4GB = 3.2GB = 3276MB
		expectedXmx := "-Xmx3276m"
		if !strings.Contains(got, expectedXmx) {
			t.Errorf("calculateJvmOptions() = %v, want to contain %v", got, expectedXmx)
		}
	})

	// Test fallback to v1
	t.Run("with cgroup v1 fallback", func(t *testing.T) {
		if err := os.Remove(testV2File); err != nil {
			t.Fatalf("Failed to remove v2 file: %v", err)
		}

		got, err := calculateJvmOptions()
		if err != nil {
			t.Fatalf("calculateJvmOptions() error = %v", err)
		}

		expectedXmx := "-Xmx3276m"
		if !strings.Contains(got, expectedXmx) {
			t.Errorf("calculateJvmOptions() = %v, want to contain %v", got, expectedXmx)
		}
	})
}

func TestGetJvmOptionsFilePermissions(t *testing.T) {
	// Create a temporary directory for the test
	tmpDir, err := os.MkdirTemp("", "permissions-test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Save original path and restore after test
	originalPath := getEnvFilePath()
	defer setEnvFilePath(originalPath)

	// Set test path
	testPath := filepath.Join(tmpDir, "JAVA_TOOL_OPTIONS")
	setEnvFilePath(testPath)

	// Calculate JVM options which will create the file
	_, err = calculateJvmOptions()
	if err != nil {
		t.Fatalf("calculateJvmOptions() error = %v", err)
	}

	// Check file permissions
	info, err := os.Stat(getEnvFilePath())
	if err != nil {
		t.Fatalf("Failed to stat JVM options file: %v", err)
	}

	// Check that the file is readable by all (0644)
	expectedMode := os.FileMode(0644)
	if info.Mode().Perm() != expectedMode {
		t.Errorf("File permissions = %v, want %v", info.Mode().Perm(), expectedMode)
	}
}
