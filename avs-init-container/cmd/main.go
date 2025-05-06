package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"

	"aerospike.com/avs-init-container/v2/util"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/yaml"
)

const (
	AVS_NODE_LABEL_KEY   = "aerospike.io/role-label"
	NODE_ROLES_KEY       = "node-roles"
	AVS_CONFIG_FILE_PATH = "/etc/aerospike-vector-search/aerospike-vector-search.yml"
	DEFAULT_HEAP_PERCENT = 65 // Default to 65% if not specified

	// Memory management constants
	DEFAULT_HEAP_FLOOR_MIB     = 1024   // 1 GiB minimum heap
	DEFAULT_HEAP_CEIL_MIB      = 262144 // 256 GiB maximum heap
	DEFAULT_DIRECT_PERCENT     = 10     // 10% for direct memory
	DEFAULT_METASPACE_MIB      = 256    // 256 MiB for metaspace
	DEFAULT_SYSTEM_RESERVE_MIB = 2048   // 2 GiB system reserve
)

// our simple tests potentially overrride these variables.
// TODO: better test fixture handling
var (
	cgroupV2File    = "/sys/fs/cgroup/memory.max"
	cgroupV1File    = "/sys/fs/cgroup/memory/memory.limit_in_bytes"
	configFilePath  = AVS_CONFIG_FILE_PATH
	jvmOptsFilePath = "/etc/aerospike-vector-search/jvm.opts"
	getConfig       = rest.InClusterConfig
)

type NodeInfoSingleton struct {
	node    *v1.Node
	service *v1.Service
	err     error
}
type NetworkMode string

const (
	NetworkModeNodePort    NetworkMode = "nodeport"
	NetworkModeHostNetwork NetworkMode = "hostnetwork"
)

var (
	instance *NodeInfoSingleton
	once     sync.Once
)

func getJvmOptions() (string, error) {
	jvmOptions := os.Getenv("AEROSPIKE_VECTOR_SEARCH_JVM_OPTIONS")
	var err error
	if jvmOptions == "" {
		jvmOptions, err = calculateJvmOptions()
		if err != nil {
			return "", fmt.Errorf("no supplied JVM options and failed to calculate options: %v", err)
		}
	}
	return jvmOptions, nil
}

func getEnvInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if i, err := strconv.Atoi(v); err == nil {
			return i
		}
	}
	return def
}

func clamp(x, lo, hi uint64) uint64 {
	if x < lo {
		return lo
	}
	if x > hi {
		return hi
	}
	return x
}

/*
calculateJvmOptions determines optimal JVM memory settings for the container.

Memory sizing logic:
 1. Detect the memory limit using (in order):
    - cgroup v2 (if available)
    - cgroup v1 (if v2 is unavailable)
    - system memory (if no cgroup limit is set)
 2. Subtract a system reserve (for OS, kubelet, etc).
 3. Calculate the JVM heap as a percentage of the remaining memory,
    but clamp it between a minimum (floor) and maximum (ceiling).
 4. Set direct memory and metaspace limits as a percentage or fixed value.
 5. Return JVM options string with all calculated values.

All thresholds and percentages are configurable via environment variables,
which are typically set via Helm values in production deployments.
*/
func calculateJvmOptions() (string, error) {
	// Get memory configuration from environment or use defaults
	heapPercent := getEnvInt("AEROSPIKE_VECTOR_SEARCH_HEAP_PERCENT", DEFAULT_HEAP_PERCENT)
	heapFloorMiB := getEnvInt("AEROSPIKE_VECTOR_SEARCH_HEAP_FLOOR_MIB", DEFAULT_HEAP_FLOOR_MIB)
	heapCeilMiB := getEnvInt("AEROSPIKE_VECTOR_SEARCH_HEAP_CEIL_MIB", DEFAULT_HEAP_CEIL_MIB)
	directPercent := getEnvInt("AEROSPIKE_VECTOR_SEARCH_DIRECT_PERCENT", DEFAULT_DIRECT_PERCENT)
	metaspaceMiB := getEnvInt("AEROSPIKE_VECTOR_SEARCH_METASPACE_MIB", DEFAULT_METASPACE_MIB)
	systemReserveMiB := getEnvInt("AEROSPIKE_VECTOR_SEARCH_SYSTEM_RESERVE_MIB", DEFAULT_SYSTEM_RESERVE_MIB)

	// Detect memory limit
	var memLimitBytes uint64
	if _, err := os.Stat(cgroupV2File); err == nil {
		memLimitBytes, err = readCgroupMemoryLimit(cgroupV2File)
		if err != nil {
			log.Printf("Warning: Failed to read cgroup v2 memory limit: %v", err)
		}
	}

	if memLimitBytes == 0 {
		if _, err := os.Stat(cgroupV1File); err == nil {
			memLimitBytes, err = readCgroupMemoryLimit(cgroupV1File)
			if err != nil {
				log.Printf("Warning: Failed to read cgroup v1 memory limit: %v", err)
			}
		}
	}

	if memLimitBytes == 0 {
		var si syscall.Sysinfo_t
		if err := Sysinfo(&si); err != nil {
			return "", fmt.Errorf("failed to get system memory info: %v", err)
		}
		memLimitBytes = uint64(si.Totalram)
		log.Printf("Using system total memory: %d bytes", memLimitBytes)
	}

	// Handle "no real limit" case
	if memLimitBytes > 9000000000000000000 {
		var si syscall.Sysinfo_t
		if err := Sysinfo(&si); err != nil {
			return "", fmt.Errorf("failed to get system memory info: %v", err)
		}
		memLimitBytes = uint64(si.Totalram)
		log.Printf("No real memory limit set, using system total memory: %d bytes", memLimitBytes)
	}

	// Subtract system reserve
	reserveBytes := uint64(systemReserveMiB) * 1024 * 1024
	if memLimitBytes > reserveBytes {
		memLimitBytes -= reserveBytes
	}

	// Calculate memory sizes
	heapBytes := clamp(
		memLimitBytes*uint64(heapPercent)/100,
		uint64(heapFloorMiB)*1024*1024,
		uint64(heapCeilMiB)*1024*1024,
	)
	directBytes := memLimitBytes * uint64(directPercent) / 100
	metaBytes := uint64(metaspaceMiB) * 1024 * 1024

	// Create JVM options
	jvmOptions := []string{
		fmt.Sprintf("-Xmx%dm", heapBytes/1024/1024),
		fmt.Sprintf("-XX:MaxDirectMemorySize=%dm", directBytes/1024/1024),
		fmt.Sprintf("-XX:MaxMetaspaceSize=%dm", metaBytes/1024/1024),
		"-XX:+ExitOnOutOfMemoryError",
		"-XX:+UseZGC",
		"-XX:+ZGenerational",
		"-Xss256k",
	}

	jvmOptionsStr := strings.Join(jvmOptions, " ")
	log.Printf("Calculated JVM options: %s (heap: %d%% of %d bytes, clamped between %d MiB and %d MiB)",
		jvmOptionsStr, heapPercent, memLimitBytes, heapFloorMiB, heapCeilMiB)

	return jvmOptionsStr, nil
}

func readCgroupMemoryLimit(path string) (uint64, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}

	// Handle "max" value in cgroup v2
	if strings.TrimSpace(string(data)) == "max" {
		// If "max" is set, we'll use the system memory instead
		var si syscall.Sysinfo_t
		if err := Sysinfo(&si); err != nil {
			return 0, fmt.Errorf("failed to get system memory info: %v", err)
		}
		return uint64(si.Totalram), nil
	}

	// Parse the memory limit value
	limit, err := strconv.ParseUint(strings.TrimSpace(string(data)), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("failed to parse memory limit value: %v", err)
	}

	return limit, nil
}

func getEndpointByMode() (string, int32, error) {
	// Default to nodeport if NETWORK_MODE is not set.
	// We will try to get the nodeport from the service if it is a NodePort service.
	// Otherwise, we will default to internal networking.
	// If NETWORK_MODE is set to hostnetwork, we will use the CONTAINER_PORT environment
	// variable to get the port.
	modeStr := os.Getenv("NETWORK_MODE")
	var networkMode NetworkMode
	if modeStr == "" {
		networkMode = NetworkModeNodePort
	} else {
		networkMode = NetworkMode(modeStr)
	}
	log.Printf("Operating in NETWORK_MODE: %s", networkMode)

	node, service, err := GetNodeInstance()
	if err != nil {
		return "", 0, err
	}

	// Determine node IP: prefer external, then internal.
	var nodeIP string
	for _, addr := range node.Status.Addresses {
		if addr.Type == v1.NodeExternalIP {
			nodeIP = addr.Address
			break
		}
	}
	if nodeIP == "" {
		for _, addr := range node.Status.Addresses {
			if addr.Type == v1.NodeInternalIP {
				nodeIP = addr.Address
				break
			}
		}
	}
	if nodeIP == "" {
		return "", 0, fmt.Errorf("no valid node IP found")
	}

	switch networkMode {
	case NetworkModeNodePort:
		if service != nil && service.Spec.Type == v1.ServiceTypeNodePort {
			var nodePort int32
			for _, port := range service.Spec.Ports {
				if port.NodePort != 0 {
					nodePort = port.NodePort
					log.Printf("Found node port: %d", nodePort)
					break
				}
			}
			if nodePort != 0 {
				return nodeIP, nodePort, nil
			}
			log.Println("NodePort not found; defaulting to internal networking (no advertised listener update)")
			return "", 0, nil
		}
		log.Println("Service is nil or not NodePort; defaulting to internal networking (no advertised listener update)")
		return "", 0, nil

	case NetworkModeHostNetwork:
		containerPortStr := os.Getenv("CONTAINER_PORT")
		if containerPortStr == "" {
			return "", 0, fmt.Errorf("CONTAINER_PORT environment variable is not set for hostnetwork mode")
		}
		containerPort, err := strconv.Atoi(containerPortStr)
		if err != nil {
			return "", 0, fmt.Errorf("invalid CONTAINER_PORT value: %s", containerPortStr)
		}
		if containerPort < 0 || containerPort > 65535 {
			return "", 0, fmt.Errorf("CONTAINER_PORT value out of range: %d", containerPort)
		}
		log.Println("CONTAINER_PORT:", containerPortStr, "for hostnetwork mode")

		return nodeIP, int32(containerPort), nil

	default:
		return "", 0, fmt.Errorf("unsupported NETWORK_MODE: %s", networkMode)
	}
}

func GetNodeInstance() (*v1.Node, *v1.Service, error) {
	once.Do(func() {
		log.Println("Starting GetNodeInstance()")
		nodeName := os.Getenv("NODE_NAME")
		if nodeName == "" {
			err := fmt.Errorf("NODE_NAME environment variable is not set")
			log.Println("Error:", err)
			instance = &NodeInfoSingleton{err: err}
			return
		}
		log.Printf("NODE_NAME: %s\n", nodeName)

		serviceName := os.Getenv("SERVICE_NAME")
		if serviceName == "" {
			err := fmt.Errorf("SERVICE_NAME environment variable is not set")
			log.Println("Error:", err)
			instance = &NodeInfoSingleton{err: err}
			return
		}
		log.Printf("SERVICE_NAME: %s\n", serviceName)

		podNamespace := os.Getenv("POD_NAMESPACE")
		if podNamespace == "" {
			err := fmt.Errorf("POD_NAMESPACE environment variable is not set")
			log.Println("Error:", err)
			instance = &NodeInfoSingleton{err: err}
			return
		}
		log.Printf("POD_NAMESPACE: %s\n", podNamespace)

		config, err := getConfig()
		if err != nil {
			log.Println("Error getting in-cluster config:", err)
			instance = &NodeInfoSingleton{err: err}
			return
		}
		log.Println("In-cluster config obtained successfully")

		clientset, err := kubernetes.NewForConfig(config)
		if err != nil {
			log.Println("Error creating clientset:", err)
			instance = &NodeInfoSingleton{err: err}
			return
		}

		node, err := clientset.CoreV1().Nodes().Get(context.TODO(), nodeName, metav1.GetOptions{})
		if err != nil {
			log.Println("Error getting node:", err)
			instance = &NodeInfoSingleton{err: err}
			return
		}
		log.Printf("Fetched node: %s\n", nodeName)

		service, err := clientset.CoreV1().Services(podNamespace).Get(context.TODO(), serviceName, metav1.GetOptions{})
		if err != nil {
			if errors.IsNotFound(err) {
				log.Printf("Service %s not found in namespace %s; proceeding with node only\n", serviceName, podNamespace)
				instance = &NodeInfoSingleton{node: node}
			} else {
				log.Println("Error getting service:", err)
				instance = &NodeInfoSingleton{err: err}
				return
			}
		} else {
			log.Printf("Fetched service: %s in namespace %s\n", serviceName, podNamespace)
			instance = &NodeInfoSingleton{node: node, service: service}
		}
	})

	return instance.node, instance.service, instance.err
}

func setAdvertisedListeners(aerospikeVectorSearchConfig map[string]interface{}) error {
	log.Println("Starting setAdvertisedListeners()")

	// Use the new function to get the endpoint based on network mode.
	ip, port, err := getEndpointByMode()
	if err != nil {
		log.Println("Error getting endpoint:", err)
		return err
	}

	if ip == "" && port == 0 {
		log.Println("No endpoint available; nothing to update")
		return nil
	}

	log.Printf("Setting advertised listeners to IP: %s, Port: %d\n", ip, port)

	serviceConfig, ok := aerospikeVectorSearchConfig["service"].(map[string]interface{})
	if !ok {
		err := fmt.Errorf("was not able to get service configuration")
		log.Println("Error:", err)
		return err
	}

	ports, ok := serviceConfig["ports"].(map[string]interface{})
	if !ok {
		err := fmt.Errorf("was not able to get service.ports configuration")
		log.Println("Error:", err)
		return err
	}

	for portName, portConfig := range ports {
		portMap, ok := portConfig.(map[string]interface{})
		if !ok {
			log.Printf("Skipping port %s due to invalid format\n", portName)
			continue
		}
		log.Printf("Setting advertised-listeners for port %s\n", portName)
		portMap["advertised-listeners"] = map[string][]map[string]interface{}{
			"default": {
				{
					"address": ip,
					"port":    port,
				},
			},
		}
	}

	updatedPorts, err := json.MarshalIndent(ports, "", "  ")
	if err == nil {
		log.Printf("Updated ports configuration: %s\n", string(updatedPorts))
	} else {
		log.Println("Error marshalling updated ports config:", err)
	}

	return nil
}

func getNodeLabels() (string, error) {
	log.Println("Starting getNodeLabels()")
	node, _, err := GetNodeInstance()
	if err != nil {
		log.Println("Error in GetNodeInstance while getting node labels:", err)
		return "", err
	}

	if label, ok := node.Labels[AVS_NODE_LABEL_KEY]; ok {
		log.Printf("Found node label: %s=%s\n", AVS_NODE_LABEL_KEY, label)
		return label, nil
	}

	log.Printf("Node label %s not found\n", AVS_NODE_LABEL_KEY)
	return "", nil
}

func getAerospikeVectorSearchRoles() (map[string]interface{}, error) {
	log.Println("Starting getAerospikeVectorSearchRoles()")
	label, err := getNodeLabels()
	if err != nil {
		log.Println("Error getting node labels:", err)
		return nil, err
	}

	if label == "" {
		log.Println("No node label found; skipping roles")
		return nil, nil
	}

	envRoles := os.Getenv("AEROSPIKE_VECTOR_SEARCH_NODE_ROLES")
	if envRoles == "" {
		log.Println("AEROSPIKE_VECTOR_SEARCH_NODE_ROLES environment variable not set; skipping roles")
		return nil, nil
	}

	var roles map[string]interface{}
	err = json.Unmarshal([]byte(envRoles), &roles)
	if err != nil {
		log.Println("Error unmarshalling AEROSPIKE_VECTOR_SEARCH_NODE_ROLES:", err)
		return nil, err
	}

	log.Printf("Available roles: %v", roles)
	if role, ok := roles[label]; ok {
		log.Printf("Found role for label %s: %v\n", label, role)
		return map[string]interface{}{NODE_ROLES_KEY: role}, nil
	}

	log.Printf("No role found for label %s\n", label)
	return nil, nil
}

func setRoles(aerospikeVectorSearchConfig map[string]interface{}) error {
	log.Println("Starting setRoles()")
	roles, err := getAerospikeVectorSearchRoles()
	if err != nil {
		log.Println("Error getting roles:", err)
		return err
	}

	if roles == nil {
		log.Println("No roles found; nothing to set")
		return nil
	}

	cluster, ok := aerospikeVectorSearchConfig["cluster"].(map[string]interface{})
	if !ok {
		err := fmt.Errorf("was not able to get cluster configuration")
		log.Println("Error:", err)
		return err
	}

	cluster[NODE_ROLES_KEY] = roles[NODE_ROLES_KEY]
	aerospikeVectorSearchConfig["cluster"] = cluster
	log.Println("Successfully set roles in cluster configuration")
	return nil
}

func getHeartbeatSeeds(aerospikeVectorSearchConfig map[string]interface{}) (map[string]string, error) {
	log.Println("Checking for heartbeat configuration...")
	heartbeat, ok := aerospikeVectorSearchConfig["heartbeat"].(map[string]interface{})
	if !ok {
		log.Println("Heartbeat section not found in configuration (optional) - skipping heartbeat setup")
		return nil, nil
	}

	heartbeatSeedList, ok := heartbeat["seeds"].([]interface{})
	if !ok || len(heartbeatSeedList) == 0 {
		log.Println("No seeds found in heartbeat configuration (optional) - skipping heartbeat setup")
		return nil, nil
	}

	heartbeatSeed, ok := heartbeatSeedList[0].(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("Failed to retrieve heartbeat seed list element")
	}

	heartbeatSeedDnsName, ok := heartbeatSeed["address"]
	if !ok {
		return nil, fmt.Errorf("Failed to retrieve heartbeat seed DNS name")
	}

	heartbeatSeedPort, ok := heartbeatSeed["port"]
	if !ok {
		return nil, fmt.Errorf("Failed to retrieve heartbeat seed port number")
	}

	log.Printf("Found heartbeat seed configuration: address=%v, port=%v", heartbeatSeedDnsName, heartbeatSeedPort)
	return map[string]string{
		"address": fmt.Sprintf("%v", heartbeatSeedDnsName),
		"port":    fmt.Sprintf("%v", heartbeatSeedPort),
	}, nil
}

func getDnsNameFormat(heartbeatSeedDnsName string) (string, string, error) {

	heartbeatSeedDnsNameParts := strings.Split(heartbeatSeedDnsName, ".")
	pod_name := heartbeatSeedDnsNameParts[0][0 : len(heartbeatSeedDnsNameParts[0])-2]

	switch len(heartbeatSeedDnsNameParts) {
	case 2:

		return pod_name, "%s-%d" + "." + heartbeatSeedDnsNameParts[1], nil
	case 6:
		if heartbeatSeedDnsNameParts[3] == "svc" && heartbeatSeedDnsNameParts[4] == "cluster" && heartbeatSeedDnsNameParts[5] == "local" {
			heartbeatSeedDnsNameFormat := fmt.Sprintf(
				"%s.%s.%s.%s.%s",
				heartbeatSeedDnsNameParts[1],
				heartbeatSeedDnsNameParts[2],
				heartbeatSeedDnsNameParts[3],
				heartbeatSeedDnsNameParts[4],
				heartbeatSeedDnsNameParts[5],
			)

			return pod_name, "%s-%d" + "." + heartbeatSeedDnsNameFormat, nil
		}
	}

	return "", "", fmt.Errorf("Invalid DNS name format")
}

func generateHeartbeatSeedsDnsNames(aerospikeVectorSearchConfig map[string]interface{}) ([]map[string]string, error) {
	log.Println("Generating heartbeat seed DNS names...")
	heartbeatSeeds, err := getHeartbeatSeeds(aerospikeVectorSearchConfig)
	if err != nil {
		return nil, err
	}
	if heartbeatSeeds == nil {
		log.Println("No heartbeat seeds to process - skipping DNS name generation")
		return nil, nil
	}

	replicasEnvVariable := os.Getenv("REPLICAS")
	if replicasEnvVariable == "" {
		return nil, fmt.Errorf("REPLICAS env variable is empty")
	}
	podNameEnvVariable := os.Getenv("POD_NAME")
	if podNameEnvVariable == "" {
		return nil, fmt.Errorf("POD_NAME env variable is empty")
	}
	parts := strings.Split(podNameEnvVariable, "-")
	if len(parts) <= 1 {
		return nil, fmt.Errorf("POD_NAME env variable has no decimal part")
	}

	pod_id, err := strconv.Atoi(parts[len(parts)-1])
	if err != nil {
		return nil, err
	}

	fmt.Printf("Pod ID: %d\n", pod_id)

	replicas, err := strconv.Atoi(replicasEnvVariable)
	if err != nil {
		return nil, err
	}

	pod_name, heartbeatSeedDnsNameFormat, err := getDnsNameFormat(heartbeatSeeds["address"])
	if err != nil {
		return nil, err
	}

	heartbeatSeedDnsNames := make([]map[string]string, 0, replicas-1)

	for i := 0; i < replicas; i++ {
		if pod_id == i {
			continue
		}
		heartbeatSeedDnsNames = append(heartbeatSeedDnsNames, map[string]string{
			"address": fmt.Sprintf(heartbeatSeedDnsNameFormat, pod_name, i),
			"port":    heartbeatSeeds["port"],
		})
	}

	return heartbeatSeedDnsNames, nil
}

func setHeartbeatSeeds(aerospikeVectorSearchConfig map[string]interface{}) error {
	log.Println("Setting up heartbeat seeds...")
	heartbeatSeedDnsNames, err := generateHeartbeatSeedsDnsNames(aerospikeVectorSearchConfig)
	if err != nil {
		return err
	}
	if heartbeatSeedDnsNames == nil {
		log.Println("No heartbeat seed DNS names to configure - configuration will not include heartbeat section")
		return nil
	}

	// Create heartbeat section if it doesn't exist
	heartbeat, ok := aerospikeVectorSearchConfig["heartbeat"].(map[string]interface{})
	if !ok {
		log.Println("Creating new heartbeat section in configuration")
		heartbeat = make(map[string]interface{})
		aerospikeVectorSearchConfig["heartbeat"] = heartbeat
	}
	heartbeat["seeds"] = heartbeatSeedDnsNames
	log.Printf("Successfully configured heartbeat with %d seed(s)", len(heartbeatSeedDnsNames))
	return nil
}

func writeConfig(config map[string]interface{}) error {
	// Moving validation to its own PR
	// // Validate mandatory fields
	// mandatoryFields := []string{"cluster", "feature-key-file", "service", "interconnect"} // TODO: aerospike/storage not included.
	// for _, field := range mandatoryFields {
	// 	if _, ok := config[field]; !ok {
	// 		return fmt.Errorf("missing mandatory field: %s", field)
	// 	}
	// }

	// // Validate cluster configuration
	// if cluster, ok := config["cluster"].(map[string]interface{}); ok {
	// 	if _, ok := cluster["cluster-name"]; !ok {
	// 		return fmt.Errorf("missing mandatory field: cluster.cluster-name")
	// 	}
	// } else {
	// 	return fmt.Errorf("invalid cluster configuration")
	// }

	// // Validate aerospike configuration
	// if aerospike, ok := config["aerospike"].(map[string]interface{}); ok {
	// 	if seeds, ok := aerospike["seeds"].([]interface{}); ok {
	// 		if len(seeds) == 0 {
	// 			return fmt.Errorf("aerospike.seeds cannot be empty")
	// 		}
	// 	} else {
	// 		return fmt.Errorf("invalid aerospike.seeds configuration")
	// 	}
	// } else {
	// 	return fmt.Errorf("invalid aerospike configuration")
	// }

	// // Validate service configuration
	// if service, ok := config["service"].(map[string]interface{}); ok {
	// 	if ports, ok := service["ports"].(map[string]interface{}); ok {
	// 		if len(ports) == 0 {
	// 			return fmt.Errorf("service.ports cannot be empty")
	// 		}
	// 	} else {
	// 		return fmt.Errorf("invalid service.ports configuration")
	// 	}
	// } else {
	// 	return fmt.Errorf("invalid service configuration")
	// }

	// // Validate interconnect configuration
	// if interconnect, ok := config["interconnect"].(map[string]interface{}); ok {
	// 	if ports, ok := interconnect["ports"].(map[string]interface{}); ok {
	// 		if len(ports) == 0 {
	// 			return fmt.Errorf("interconnect.ports cannot be empty")
	// 		}
	// 	} else {
	// 		return fmt.Errorf("invalid interconnect.ports configuration")
	// 	}
	// } else {
	// 	return fmt.Errorf("invalid interconnect configuration")
	// }

	// Convert config to YAML
	data, err := yaml.Marshal(config)
	if err != nil {
		return fmt.Errorf("failed to marshal config: %v", err)
	}

	// Create directory if it doesn't exist
	dir := filepath.Dir(configFilePath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("failed to create config directory: %v", err)
	}

	// Write config file
	if err := os.WriteFile(configFilePath, data, 0644); err != nil {
		return fmt.Errorf("failed to write config file: %v", err)
	}

	return nil
}

func writeJvmOptions(opts string) error {
	// Create the directory if it doesn't exist
	if err := os.MkdirAll(filepath.Dir(jvmOptsFilePath), 0755); err != nil {
		return fmt.Errorf("failed to create directory for JVM options file: %v", err)
	}

	// Write the options to file with 0644 permissions
	if err := os.WriteFile(jvmOptsFilePath, []byte(opts), 0644); err != nil {
		return fmt.Errorf("failed to write JVM options file: %v", err)
	}

	return nil
}

// CalculateMemoryConfig calculates JVM heap and direct memory sizes based on system memory and configuration
func CalculateMemoryConfig() (heapMiB int, directMiB int, err error) {
	var si syscall.Sysinfo_t
	if err := Sysinfo(&si); err != nil {
		return 0, 0, fmt.Errorf("failed to get system memory info: %v", err)
	}

	// Get memory configuration from environment variables
	heapPercent, err := strconv.Atoi(os.Getenv("AEROSPIKE_VECTOR_SEARCH_HEAP_PERCENT"))
	if err != nil {
		return 0, 0, fmt.Errorf("invalid heap percent: %v", err)
	}
	if heapPercent < 1 || heapPercent > 100 {
		return 0, 0, fmt.Errorf("heap percent must be between 1 and 100")
	}

	heapFloorMiB, err := strconv.Atoi(os.Getenv("AEROSPIKE_VECTOR_SEARCH_HEAP_FLOOR_MIB"))
	if err != nil {
		return 0, 0, fmt.Errorf("invalid heap floor: %v", err)
	}
	if heapFloorMiB < 1 {
		return 0, 0, fmt.Errorf("heap floor must be at least 1 MiB")
	}

	heapCeilMiB, err := strconv.Atoi(os.Getenv("AEROSPIKE_VECTOR_SEARCH_HEAP_CEIL_MIB"))
	if err != nil {
		return 0, 0, fmt.Errorf("invalid heap ceiling: %v", err)
	}
	if heapCeilMiB < 1 {
		return 0, 0, fmt.Errorf("heap ceiling must be at least 1 MiB")
	}

	directPercent, err := strconv.Atoi(os.Getenv("AEROSPIKE_VECTOR_SEARCH_DIRECT_PERCENT"))
	if err != nil {
		return 0, 0, fmt.Errorf("invalid direct percent: %v", err)
	}
	if directPercent < 0 || directPercent > 100 {
		return 0, 0, fmt.Errorf("direct percent must be between 0 and 100")
	}

	systemReserveMiB, err := strconv.Atoi(os.Getenv("AEROSPIKE_VECTOR_SEARCH_SYSTEM_RESERVE_MIB"))
	if err != nil {
		return 0, 0, fmt.Errorf("invalid system reserve: %v", err)
	}
	if systemReserveMiB < 0 {
		return 0, 0, fmt.Errorf("system reserve must be non-negative")
	}

	// Calculate available memory in MiB
	totalMiB := int(si.Totalram / (1024 * 1024))
	availableMiB := totalMiB - systemReserveMiB
	if availableMiB < 0 {
		availableMiB = 0
	}

	// Calculate heap size
	calculatedHeapMiB := (availableMiB * heapPercent) / 100
	heapMiB = int(clamp(uint64(calculatedHeapMiB), uint64(heapFloorMiB), uint64(heapCeilMiB)))

	// Calculate direct memory size
	directMiB = (availableMiB * directPercent) / 100

	return heapMiB, directMiB, nil
}

func run() int {
	log.Println("Init container started")

	configEnv := os.Getenv("AEROSPIKE_VECTOR_SEARCH_CONFIG")
	if configEnv == "" {
		log.Println("AEROSPIKE_VECTOR_SEARCH_CONFIG environment variable is not set")
		return 1
	}

	var aerospikeVectorSearchConfig map[string]interface{}
	err := json.Unmarshal([]byte(configEnv), &aerospikeVectorSearchConfig)
	if err != nil {
		log.Println("Error unmarshalling AEROSPIKE_VECTOR_SEARCH_CONFIG:", err)
		return util.ToExitVal(err)
	}

	configDump, _ := json.MarshalIndent(aerospikeVectorSearchConfig, "", "  ")
	log.Printf("Initial configuration:\n%s\n", string(configDump))

	err = setRoles(aerospikeVectorSearchConfig)
	if err != nil {
		log.Println("Error setting roles:", err)
		return util.ToExitVal(err)
	}

	err = setAdvertisedListeners(aerospikeVectorSearchConfig)
	if err != nil {
		log.Println("Error setting advertised listeners:", err)
		return util.ToExitVal(err)
	}

	err = setHeartbeatSeeds(aerospikeVectorSearchConfig)
	if err != nil {
		log.Println("Error setting heartbeat:", err)
		return util.ToExitVal(err)
	}

	err = writeConfig(aerospikeVectorSearchConfig)
	if err != nil {
		log.Println("Error writing config:", err)
		return util.ToExitVal(err)
	}

	// Handle JVM options at the end
	jvmOpts, err := getJvmOptions()
	if err != nil {
		log.Printf("Error getting JVM options: %v", err)
		return util.ToExitVal(err)
	}

	// Set environment variable
	if err := os.Setenv("JAVA_OPTS", jvmOpts); err != nil {
		log.Printf("Error setting JAVA_OPTS: %v", err)
		return util.ToExitVal(err)
	}
	log.Printf("Set JAVA_OPTS to: %s", jvmOpts)

	// Write JVM options to file
	if err := writeJvmOptions(jvmOpts); err != nil {
		log.Printf("Error writing JVM options to file: %v", err)
		return util.ToExitVal(err)
	}
	log.Printf("Wrote JVM options to file: %s", jvmOptsFilePath)

	log.Println("Init container completed successfully")
	return util.ToExitVal(nil)
}

func main() {
	os.Exit(run())
}
