package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"

	"aerospike.com/avs-init-container/v2/util"
	"go.uber.org/zap"
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
	logger   *zap.Logger
)

func initLogger() {
	var err error
	logger, err = zap.NewProduction()
	if err != nil {
		panic(fmt.Sprintf("Failed to initialize logger: %v", err))
	}
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
	logger.Info("Operating in NETWORK_MODE", zap.String("mode", string(networkMode)))

	node, service, err := GetNodeInstance()
	if err != nil {
		return "", 0, err
	}

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
			for _, port := range service.Spec.Ports {
				if port.NodePort != 0 {
					logger.Info("Found node port", zap.Int32("port", port.NodePort))
					return nodeIP, port.NodePort, nil
				}
			}
			logger.Warn("NodePort not found; defaulting to internal networking")
			return "", 0, nil
		}
		logger.Warn("Service is nil or not NodePort; defaulting to internal networking")
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
		logger.Info("Using CONTAINER_PORT", zap.Int("port", containerPort))
		return nodeIP, int32(containerPort), nil

	default:
		return "", 0, fmt.Errorf("unsupported NETWORK_MODE: %s", networkMode)
	}
}

func GetNodeInstance() (*v1.Node, *v1.Service, error) {
	once.Do(func() {
		logger.Info("Starting GetNodeInstance")

		nodeName := os.Getenv("NODE_NAME")
		if nodeName == "" {
			err := fmt.Errorf("NODE_NAME environment variable is not set")
			logger.Error("Error", zap.Error(err))
			instance = &NodeInfoSingleton{err: err}
			return
		}
		logger.Info("NODE_NAME", zap.String("nodeName", nodeName))

		serviceName := os.Getenv("SERVICE_NAME")
		if serviceName == "" {
			err := fmt.Errorf("SERVICE_NAME environment variable is not set")
			logger.Error("Error", zap.Error(err))
			instance = &NodeInfoSingleton{err: err}
			return
		}
		logger.Info("SERVICE_NAME", zap.String("serviceName", serviceName))

		podNamespace := os.Getenv("POD_NAMESPACE")
		if podNamespace == "" {
			err := fmt.Errorf("POD_NAMESPACE environment variable is not set")
			logger.Error("Error", zap.Error(err))
			instance = &NodeInfoSingleton{err: err}
			return
		}
		logger.Info("POD_NAMESPACE", zap.String("namespace", podNamespace))

		config, err := rest.InClusterConfig()
		if err != nil {
			logger.Error("Error getting in-cluster config", zap.Error(err))
			instance = &NodeInfoSingleton{err: err}
			return
		}
		logger.Info("In-cluster config obtained successfully")

		clientset, err := kubernetes.NewForConfig(config)
		if err != nil {
			logger.Error("Error creating clientset", zap.Error(err))
			instance = &NodeInfoSingleton{err: err}
			return
		}

		node, err := clientset.CoreV1().Nodes().Get(context.TODO(), nodeName, metav1.GetOptions{})
		if err != nil {
			logger.Error("Error getting node", zap.Error(err))
			instance = &NodeInfoSingleton{err: err}
			return
		}
		logger.Info("Fetched node", zap.String("nodeName", nodeName))

		service, err := clientset.CoreV1().Services(podNamespace).Get(context.TODO(), serviceName, metav1.GetOptions{})
		if err != nil {
			if errors.IsNotFound(err) {
				logger.Warn("Service not found; proceeding with node only", zap.String("serviceName", serviceName), zap.String("namespace", podNamespace))
				instance = &NodeInfoSingleton{node: node}
			} else {
				logger.Error("Error getting service", zap.Error(err))
				instance = &NodeInfoSingleton{err: err}
				return
			}
		} else {
			logger.Info("Fetched service", zap.String("serviceName", serviceName), zap.String("namespace", podNamespace))
			instance = &NodeInfoSingleton{node: node, service: service}
		}
	})

	return instance.node, instance.service, instance.err
}

func setAdvertisedListeners(aerospikeVectorSearchConfig map[string]interface{}) error {
	logger.Info("Starting setAdvertisedListeners")

	ip, port, err := getEndpointByMode()
	if err != nil {
		logger.Error("Error getting endpoint", zap.Error(err))
		return err
	}

	if ip == "" && port == 0 {
		logger.Warn("No endpoint available; nothing to update")
		return nil
	}

	logger.Info("Setting advertised listeners", zap.String("IP", ip), zap.Int32("Port", port))

	serviceConfig, ok := aerospikeVectorSearchConfig["service"].(map[string]interface{})
	if !ok {
		err := fmt.Errorf("was not able to get service configuration")
		logger.Error("Error accessing service configuration", zap.Error(err))
		return err
	}

	ports, ok := serviceConfig["ports"].(map[string]interface{})
	if !ok {
		err := fmt.Errorf("was not able to get service.ports configuration")
		logger.Error("Error accessing service ports configuration", zap.Error(err))
		return err
	}

	for portName, portConfig := range ports {
		portMap, ok := portConfig.(map[string]interface{})
		if !ok {
			logger.Warn("Skipping port due to invalid format", zap.String("port", portName))
			continue
		}
		logger.Info("Setting advertised-listeners for port", zap.String("port", portName))
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
		logger.Info("Updated ports configuration", zap.String("config", string(updatedPorts)))
	} else {
		logger.Error("Error marshalling updated ports config", zap.Error(err))
	}

	return nil
}

func getNodeLabels() (string, error) {
	logger.Info("Starting getNodeLabels")

	node, _, err := GetNodeInstance()
	if err != nil {
		logger.Error("Error in GetNodeInstance while getting node labels", zap.Error(err))
		return "", err
	}

	if label, ok := node.Labels[AVS_NODE_LABEL_KEY]; ok {
		logger.Info("Found node label", zap.String("key", AVS_NODE_LABEL_KEY), zap.String("label", label))
		return label, nil
	}

	logger.Warn("Node label not found", zap.String("key", AVS_NODE_LABEL_KEY))
	return "", nil
}

func getAerospikeVectorSearchRoles() (map[string]interface{}, error) {
	logger.Info("Starting getAerospikeVectorSearchRoles")

	label, err := getNodeLabels()
	if err != nil {
		logger.Error("Error getting node labels", zap.Error(err))
		return nil, err
	}

	if label == "" {
		logger.Warn("No node label found; skipping roles")
		return nil, nil
	}

	envRoles := os.Getenv("AEROSPIKE_VECTOR_SEARCH_NODE_ROLES")
	if envRoles == "" {
		logger.Warn("AEROSPIKE_VECTOR_SEARCH_NODE_ROLES environment variable not set; skipping roles")
		return nil, nil
	}

	var roles map[string]interface{}
	err = json.Unmarshal([]byte(envRoles), &roles)
	if err != nil {
		logger.Error("Error unmarshalling AEROSPIKE_VECTOR_SEARCH_NODE_ROLES", zap.Error(err))
		return nil, err
	}

	if role, ok := roles[label]; ok {
		logger.Info("Found role for label", zap.String("label", label), zap.Any("role", role))
		return map[string]interface{}{NODE_ROLES_KEY: role}, nil
	}

	logger.Warn("No role found for label", zap.String("label", label))
	return nil, nil
}

func setRoles(aerospikeVectorSearchConfig map[string]interface{}) error {
	logger.Info("Starting setRoles")

	roles, err := getAerospikeVectorSearchRoles()
	if err != nil {
		logger.Error("Error getting roles", zap.Error(err))
		return err
	}

	if roles == nil {
		logger.Warn("No roles found; nothing to set")
		return nil
	}

	cluster, ok := aerospikeVectorSearchConfig["cluster"].(map[string]interface{})
	if !ok {
		err := fmt.Errorf("was not able to get cluster configuration")
		logger.Error("Error accessing cluster configuration", zap.Error(err))
		return err
	}

	cluster[NODE_ROLES_KEY] = roles[NODE_ROLES_KEY]
	aerospikeVectorSearchConfig["cluster"] = cluster
	logger.Info("Successfully set roles in cluster configuration")
	return nil
}

func getHeartbeatSeeds(aerospikeVectorSearchConfig map[string]interface{}) (map[string]string, error) {

	heartbeat, ok := aerospikeVectorSearchConfig["heartbeat"].(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("Failed to retrieve heartbeat section")
	}

	heartbeatSeedList, ok := heartbeat["seeds"].([]interface{})
	if !ok {
		return nil, fmt.Errorf("Failed to retrieve heartbeat seed list")
	}

	if len(heartbeatSeedList) == 0 {
		return nil, fmt.Errorf("Heartbeat seed list is empty")
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
	logger.Info("Generating heartbeat seed DNS names")

	replicasEnvVariable := os.Getenv("REPLICAS")
	if replicasEnvVariable == "" {
		logger.Error("REPLICAS env variable is empty")
		return nil, fmt.Errorf("REPLICAS env variable is empty")
	}

	podNameEnvVariable := os.Getenv("POD_NAME")
	if podNameEnvVariable == "" {
		logger.Error("POD_NAME env variable is empty")
		return nil, fmt.Errorf("POD_NAME env variable is empty")
	}

	parts := strings.Split(podNameEnvVariable, "-")
	if len(parts) <= 1 {
		logger.Error("POD_NAME env variable has no decimal part")
		return nil, fmt.Errorf("POD_NAME env variable has no decimal part")
	}

	pod_id, err := strconv.Atoi(parts[len(parts)-1])
	if err != nil {
		logger.Error("Error converting pod ID", zap.Error(err))
		return nil, err
	}

	logger.Info("Parsed Pod ID", zap.Int("pod_id", pod_id))

	replicas, err := strconv.Atoi(replicasEnvVariable)
	if err != nil {
		logger.Error("Error converting replicas value", zap.Error(err))
		return nil, err
	}

	heartbeatSeeds, err := getHeartbeatSeeds(aerospikeVectorSearchConfig)
	if err != nil {
		logger.Error("Error retrieving heartbeat seeds", zap.Error(err))
		return nil, err
	}

	pod_name, heartbeatSeedDnsNameFormat, err := getDnsNameFormat(heartbeatSeeds["address"])
	if err != nil {
		logger.Error("Error getting DNS name format", zap.Error(err))
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

	logger.Info("Generated heartbeat seed DNS names", zap.Any("seeds", heartbeatSeedDnsNames))
	return heartbeatSeedDnsNames, nil
}

func setHeartbeatSeeds(aerospikeVectorSearchConfig map[string]interface{}) error {

	heartbeatSeedDnsNames, err := generateHeartbeatSeedsDnsNames(aerospikeVectorSearchConfig)
	if err != nil {
		return err
	}

	if heartbeat, ok := aerospikeVectorSearchConfig["heartbeat"].(map[string]interface{}); ok {
		heartbeat["seeds"] = heartbeatSeedDnsNames
	}

	return nil
}

func writeConfig(aerospikeVectorSearchConfig map[string]interface{}) error {
	logger.Info("Starting writeConfig")

	configBytes, err := yaml.Marshal(aerospikeVectorSearchConfig)
	if err != nil {
		logger.Error("Error marshalling config to YAML", zap.Error(err))
		return err
	}

	logger.Info("Final configuration", zap.String("config", string(configBytes)))

	file, err := os.Create(AVS_CONFIG_FILE_PATH)
	if err != nil {
		logger.Error("Error creating config file", zap.Error(err))
		return err
	}
	defer func() {
		if cerr := file.Close(); cerr != nil {
			logger.Error("Error closing config file", zap.Error(cerr))
		}
	}()

	_, err = file.Write(configBytes)
	if err != nil {
		logger.Error("Error writing config file", zap.Error(err))
		return err
	}

	logger.Info("Configuration written successfully", zap.String("path", AVS_CONFIG_FILE_PATH))
	return nil
}

func run() int {

	configEnv := os.Getenv("AEROSPIKE_VECTOR_SEARCH_CONFIG")
	if configEnv == "" {
		logger.Error("AEROSPIKE_VECTOR_SEARCH_CONFIG environment variable is not set")
		return 1
	}

	var aerospikeVectorSearchConfig map[string]interface{}
	err := json.Unmarshal([]byte(configEnv), &aerospikeVectorSearchConfig)
	if err != nil {
		logger.Error("Error unmarshalling AEROSPIKE_VECTOR_SEARCH_CONFIG", zap.Error(err))
		return util.ToExitVal(err)
	}

	configDump, _ := json.MarshalIndent(aerospikeVectorSearchConfig, "", "  ")
	logger.Info("Initial configuration", zap.String("config", string(configDump)))

	err = setRoles(aerospikeVectorSearchConfig)
	if err != nil {
		logger.Error("Error setting roles", zap.Error(err))
		return util.ToExitVal(err)
	}

	err = setAdvertisedListeners(aerospikeVectorSearchConfig)
	if err != nil {
		logger.Error("Error setting advertised listeners", zap.Error(err))
		return util.ToExitVal(err)
	}

	err = setHeartbeatSeeds(aerospikeVectorSearchConfig)
	if err != nil {
		logger.Error("Error setting heartbeat", zap.Error(err))
		return util.ToExitVal(err)
	}

	err = writeConfig(aerospikeVectorSearchConfig)
	if err != nil {
		logger.Error("Error writing config", zap.Error(err))
		return util.ToExitVal(err)
	}

	logger.Info("Init container completed successfully")
	return util.ToExitVal(nil)
}

func main() {
	initLogger()
	defer logger.Sync()
	logger.Info("Init container started")
	os.Exit(run())
}
