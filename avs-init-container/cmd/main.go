package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
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

// GetNodeInstance returns the node and service objects for the current pod.
func GetNodeInstance() (*v1.Node, *v1.Service, error) {
	once.Do(func() {
		log.Println("Starting GetNodeInstance()")
		nodeName := os.Getenv("NODE_NAME")
		if nodeName == "" {
			err := fmt.Errorf("NODE_NAME environment variable is not set")
			log.Println("Error:", err)
			instance = &NodeInfoSingleton{
				node:    nil,
				service: nil,
				err:     err,
			}
			return
		}
		log.Printf("NODE_NAME: %s\n", nodeName)

		serviceName := os.Getenv("SERVICE_NAME")
		if serviceName == "" { // fixed: should check serviceName instead of nodeName again
			err := fmt.Errorf("SERVICE_NAME environment variable is not set")
			log.Println("Error:", err)
			instance = &NodeInfoSingleton{
				node:    nil,
				service: nil,
				err:     err,
			}
			return
		}
		log.Printf("SERVICE_NAME: %s\n", serviceName)

		podNamespace := os.Getenv("POD_NAMESPACE")
		if podNamespace == "" { // fixed: should check podNamespace
			err := fmt.Errorf("POD_NAMESPACE environment variable is not set")
			log.Println("Error:", err)
			instance = &NodeInfoSingleton{
				node:    nil,
				service: nil,
				err:     err,
			}
			return
		}
		log.Printf("POD_NAMESPACE: %s\n", podNamespace)

		config, err := rest.InClusterConfig()
		if err != nil {
			log.Println("Error getting in-cluster config:", err)
			instance = &NodeInfoSingleton{
				node:    nil,
				service: nil,
				err:     err,
			}
			return
		}
		log.Println("In-cluster config obtained successfully")

		clientset, err := kubernetes.NewForConfig(config)
		if err != nil {
			log.Println("Error creating clientset:", err)
			instance = &NodeInfoSingleton{
				node:    nil,
				service: nil,
				err:     err,
			}
			return
		}

		node, err := clientset.CoreV1().Nodes().Get(context.TODO(), nodeName, metav1.GetOptions{})
		if err != nil {
			log.Println("Error getting node:", err)
			instance = &NodeInfoSingleton{
				node:    nil,
				service: nil,
				err:     err,
			}
			return
		}
		log.Printf("Fetched node %s\n", nodeName)

		service, err := clientset.CoreV1().Services(podNamespace).Get(context.TODO(), serviceName, metav1.GetOptions{})
		if err != nil {
			if errors.IsNotFound(err) {
				log.Printf("Service %s not found in namespace %s; proceeding with node only\n", serviceName, podNamespace)
				instance = &NodeInfoSingleton{
					node:    node,
					service: nil,
					err:     nil,
				}
			} else {
				log.Println("Error getting service:", err)
				instance = &NodeInfoSingleton{
					node:    nil,
					service: nil,
					err:     err,
				}
				return
			}
		} else {
			log.Printf("Fetched service %s in namespace %s\n", serviceName, podNamespace)
			instance = &NodeInfoSingleton{
				node:    node,
				service: service,
				err:     nil,
			}
		}
	})

	return instance.node, instance.service, instance.err
}

// getNodeIp returns the appropriate IP and node port.
func getNodeIp() (string, int32, error) {
	log.Println("Starting getNodeIp()")
	externalIP, internalIP := "", ""

	node, service, err := GetNodeInstance()
	if err != nil {
		log.Println("Error in GetNodeInstance:", err)
		return "", 0, err
	}

	if service == nil {
		log.Println("Service is nil, returning empty IP and port")
		return "", 0, nil
	}

	if service.Spec.Type != v1.ServiceTypeNodePort {
		log.Printf("Service type is %s, not NodePort; skipping advertised listener configuration\n", service.Spec.Type)
		return "", 0, nil
	}

	var nodePort int32 = 0
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
	log.Printf("Node addresses - External: %s, Internal: %s\n", externalIP, internalIP)

	if externalIP != "" {
		return externalIP, nodePort, nil
	}

	if internalIP != "" {
		return internalIP, nodePort, nil
	}

	return "", 0, nil
}

// setAdvertisedListeners updates the configuration with the node IP and port.
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

	service, ok := aerospikeVectorSearchConfig["service"].(map[string]interface{})
	if !ok {
		err := fmt.Errorf("was not able to get service from config")
		log.Println("Error:", err)
		return err
	}

	ports, ok := service["ports"].(map[string]interface{})
	if !ok {
		err := fmt.Errorf("was not able to get service.ports from config")
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
	return nil
}

// getNodeLabels returns the label value for the node.
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

// getAerospikeVectorSearchRoles returns the node roles from the environment variable.
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

	aerospikeVectorSearchRolesEnvVariable := os.Getenv("AEROSPIKE_VECTOR_SEARCH_NODE_ROLES")
	if aerospikeVectorSearchRolesEnvVariable == "" {
		log.Println("AEROSPIKE_VECTOR_SEARCH_NODE_ROLES env variable not set; skipping roles")
		return nil, nil
	}

	var aerospikeVectorSearchRoles map[string]interface{}
	err = json.Unmarshal([]byte(aerospikeVectorSearchRolesEnvVariable), &aerospikeVectorSearchRoles)
	if err != nil {
		log.Println("Error unmarshalling AEROSPIKE_VECTOR_SEARCH_NODE_ROLES:", err)
		return nil, err
	}

	if role, ok := aerospikeVectorSearchRoles[label]; ok {
		log.Printf("Found role for label %s\n", label)
		retval := make(map[string]interface{})
		retval[NODE_ROLES_KEY] = role
		return retval, nil
	}

	log.Printf("No role found for label %s\n", label)
	return nil, nil
}

// setRoles updates the cluster configuration with the node roles.
func setRoles(aerospikeVectorSearchConfig map[string]interface{}) error {
	log.Println("Starting setRoles()")
	roles, err := getAerospikeVectorSearchRoles()
	if err != nil {
		log.Println("Error getting roles:", err)
		return err
	}

	if roles == nil {
		log.Println("No roles to set; skipping")
		return nil
	}

	if cluster, ok := aerospikeVectorSearchConfig["cluster"].(map[string]interface{}); ok {
		cluster[NODE_ROLES_KEY] = roles[NODE_ROLES_KEY]
		aerospikeVectorSearchConfig["cluster"] = cluster
		log.Println("Successfully set roles in cluster config")
	} else {
		err := fmt.Errorf("was not able to get cluster from config")
		log.Println("Error:", err)
		return err
	}

	return nil
}

// writeConfig writes the updated configuration to a file.
func writeConfig(aerospikeVectorSearchConfig map[string]interface{}) error {
	log.Println("Starting writeConfig()")
	config, err := yaml.Marshal(aerospikeVectorSearchConfig)
	if err != nil {
		log.Println("Error marshalling config to YAML:", err)
		return err
	}

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
	_, err = file.Write(config)
	if err != nil {
		log.Println("Error writing config file:", err)
		return err
	}

	log.Printf("Configuration written successfully to %s\n", AVS_CONFIG_FILE_PATH)
	return nil
}

// run is the main logic of the init container.
func run() int {
	log.Println("Init container started")

	aerospikeVectorSearchConfigEnvVariable := os.Getenv("AEROSPIKE_VECTOR_SEARCH_CONFIG")
	if aerospikeVectorSearchConfigEnvVariable == "" {
		log.Println("AEROSPIKE_VECTOR_SEARCH_CONFIG environment variable is not set")
		return 1
	}

	var aerospikeVectorSearchConfig map[string]interface{}
	err := json.Unmarshal([]byte(aerospikeVectorSearchConfigEnvVariable), &aerospikeVectorSearchConfig)
	if err != nil {
		log.Println("Error unmarshalling AEROSPIKE_VECTOR_SEARCH_CONFIG:", err)
		return util.ToExitVal(err)
	}

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

	err = writeConfig(aerospikeVectorSearchConfig)
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
