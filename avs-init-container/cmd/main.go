package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"sync"

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
)

type NodeInfoSingleton struct {
	node    *v1.Node
	service *v1.Service
	err     error
}

var (
	instance *NodeInfoSingleton
	once     sync.Once
)

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

		config, err := rest.InClusterConfig()
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

func getNodeIp() (string, int32, error) {
	log.Println("Starting getNodeIp()")
	externalIP, internalIP := "", ""

	node, service, err := GetNodeInstance()
	if err != nil {
		log.Println("Error in GetNodeInstance:", err)
		return "", 0, err
	}

	for _, addr := range node.Status.Addresses {
		log.Printf("Node address: Type=%s, Address=%s\n", addr.Type, addr.Address)
	}

	if service == nil {
		log.Println("Service is nil, skipping advertised listener configuration")
		return "", 0, nil
	}

	if service.Spec.Type != v1.ServiceTypeNodePort {
		log.Printf("Service type is %s, not NodePort; skipping advertised listener configuration\n", service.Spec.Type)
		return "", 0, nil
	}

	var nodePort int32
	for _, port := range service.Spec.Ports {
		if port.NodePort != 0 {
			nodePort = port.NodePort
			log.Printf("Found node port: %d\n", nodePort)
			break
		}
	}
	if nodePort == 0 {
		err := fmt.Errorf("node port is not set")
		log.Println("Error:", err)
		return "", 0, err
	}

	for _, addr := range node.Status.Addresses {
		if addr.Type == v1.NodeInternalIP {
			internalIP = addr.Address
		} else if addr.Type == v1.NodeExternalIP {
			externalIP = addr.Address
		}
	}
	log.Printf("Determined node addresses - External: %s, Internal: %s\n", externalIP, internalIP)

	if externalIP != "" {
		return externalIP, nodePort, nil
	}
	if internalIP != "" {
		return internalIP, nodePort, nil
	}

	log.Println("No valid IP found for node")
	return "", 0, nil
}

func setAdvertisedListeners(aerospikeVectorSearchConfig map[string]interface{}) error {
	log.Println("Starting setAdvertisedListeners()")
	nodeIP, nodePort, err := getNodeIp()
	if err != nil {
		log.Println("Error getting node IP:", err)
		return err
	}

	if nodeIP == "" && nodePort == 0 {
		log.Println("No node IP or port found; nothing to update")
		return nil
	}

	log.Printf("Setting advertised listeners to Node IP: %s, Port: %d\n", nodeIP, nodePort)

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
					"address": nodeIP,
					"port":    nodePort,
				},
			},
		}
	}

	// Log the updated ports config or .
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

	replicasEnvVariable := os.Getenv("REPLICAS")
	if replicasEnvVariable == "" {
		return nil, fmt.Errorf("REPLICAS env variable is empty")
	}

	replicas, err := strconv.Atoi(replicasEnvVariable)
	if err != nil {
		return nil, err
	}

	heartbeatSeeds, err := getHeartbeatSeeds(aerospikeVectorSearchConfig)
	if err != nil {
		return nil, err
	}

	pod_name, heartbeatSeedDnsNameFormat, err := getDnsNameFormat(heartbeatSeeds["address"])
	if err != nil {
		return nil, err
	}

	heartbeatSeedDnsNames := make([]map[string]string, replicas, replicas)

	for i := 0; i < replicas; i++ {
		heartbeatSeedDnsNames[i] = map[string]string{
			"address": fmt.Sprintf(heartbeatSeedDnsNameFormat, pod_name, i),
			"port":    heartbeatSeeds["port"],
		}
	}

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

func writeConfigFile(aerospikeVectorSearchConfig map[string]interface{}) error {

	log.Println("Starting writeConfig()")
	configBytes, err := yaml.Marshal(aerospikeVectorSearchConfig)
	if err != nil {
		log.Println("Error marshalling config to YAML:", err)
		return err
	}

	// Log the final config for debugging purposes.
	log.Printf("Final configuration:\n%s\n", string(configBytes))

	file, err := os.Create(AVS_CONFIG_FILE_PATH)
	if err != nil {
		log.Println("Error creating config file:", err)
		return err
	}
	defer func() {
		if cerr := file.Close(); cerr != nil {
			log.Println("Error closing config file:", cerr)
		}
	}()

	_, err = file.Write(configBytes)
	if err != nil {
		log.Println("Error writing config file:", err)
		return err
	}

	log.Printf("Configuration written successfully to %s\n", AVS_CONFIG_FILE_PATH)
	return nil
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

	// Log the incoming configuration for reference.
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
		log.Println("Error setting seed list:", err)
		return util.ToExitVal(err)
	}

	err = writeConfigFile(aerospikeVectorSearchConfig)
	if err != nil {
		log.Println("Error writing config:", err)
		return util.ToExitVal(err)
	}

	log.Println("Init container completed successfully")
	return util.ToExitVal(nil)
}

func main() {
	os.Exit(run())
}
