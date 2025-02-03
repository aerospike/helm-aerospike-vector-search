package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sync"

	"aerospike.com/avs-init-container/v2/util"
	v1 "k8s.io/api/core/v1"
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
		nodeName := os.Getenv("NODE_NAME")
		if nodeName == "" {
			instance = &NodeInfoSingleton{
				node:    nil,
				service: nil,
				err:     fmt.Errorf("NODE_NAME environment variable is not set"),
			}
			return
		}

		serviceName := os.Getenv("SERVICE_NAME")
		if nodeName == "" {
			instance = &NodeInfoSingleton{
				node:    nil,
				service: nil,
				err:     fmt.Errorf("POD_NAME environment variable is not set"),
			}
			return
		}

		podNamespace := os.Getenv("POD_NAMESPACE")
		if nodeName == "" {
			instance = &NodeInfoSingleton{
				node:    nil,
				service: nil,
				err:     fmt.Errorf("POD_NAMESPACE environment variable is not set"),
			}
			return
		}

		config, err := rest.InClusterConfig()
		if err != nil {
			instance = &NodeInfoSingleton{
				node:    nil,
				service: nil,
				err:     err,
			}
			return
		}

		clientset, err := kubernetes.NewForConfig(config)
		if err != nil {
			instance = &NodeInfoSingleton{
				node:    nil,
				service: nil,
				err:     err,
			}
			return
		}

		node, err := clientset.CoreV1().Nodes().Get(context.TODO(), nodeName, metav1.GetOptions{})
		if err != nil {
			instance = &NodeInfoSingleton{
				node:    nil,
				service: nil,
				err:     err,
			}
			return
		}

		service, err := clientset.CoreV1().Services(podNamespace).Get(context.TODO(), serviceName, metav1.GetOptions{})
		if err != nil {
			instance = &NodeInfoSingleton{
				node:    nil,
				service: nil,
				err:     err,
			}
			return
		}

		instance = &NodeInfoSingleton{
			node:    node,
			service: service,
			err:     err,
		}
	})

	return instance.node, instance.service, instance.err
}

func getNodeIp() (string, int32, error) {

	externalIP, internalIP := "", ""

	node, service, err := GetNodeInstance()
	if err != nil {
		return "", 0, err
	}

	var nodePort int32 = 0
	for _, port := range service.Spec.Ports {
		if port.NodePort != 0 {
			nodePort = port.NodePort
			break
		}
	}
	if nodePort == 0 {
		return "", 0, fmt.Errorf("node port is not set")
	}

	for _, addr := range node.Status.Addresses {
		if addr.Type == v1.NodeInternalIP {
			internalIP = addr.Address
		} else if addr.Type == v1.NodeExternalIP {
			externalIP = addr.Address
		}
	}

	if externalIP != "" {
		return externalIP, nodePort, nil
	}

	if internalIP != "" {
		return internalIP, nodePort, nil
	}

	return "", 0, nil
}

func setAdvertisedListeners(aerospikeVectorSearchConfig map[string]interface{}) error {

	nodeIP, nodePort, err := getNodeIp()
	if err != nil {
		return err
	}
	fmt.Printf("Node IP: %s Port: %d\n", nodeIP, nodePort)
	service, ok := aerospikeVectorSearchConfig["service"].(map[string]interface{})
	if !ok {
		return fmt.Errorf("Was not able to get service")
	}

	ports, ok := service["ports"].(map[string]interface{})
	if !ok {
		return fmt.Errorf("Was not able to get service.ports")
	}

	for _, portConfig := range ports {
		portMap, ok := portConfig.(map[string]interface{})
		if !ok {
			continue
		}

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

func getNodeLabels() (string, error) {

	node, _, err := GetNodeInstance()
	if err != nil {
		return "", err
	}

	if label, ok := node.Labels[AVS_NODE_LABEL_KEY]; ok {
		return label, nil
	}
	return "", nil
}

func getAerospikeVectorSearchRoles() (map[string]interface{}, error) {

	label, err := getNodeLabels()
	if err != nil {
		return nil, err
	}

	if label == "" {
		return nil, nil
	}

	aerospikeVectorSearchRolesEnvVariable := os.Getenv("AEROSPIKE_VECTOR_SEARCH_NODE_ROLES")
	if aerospikeVectorSearchRolesEnvVariable == "" {
		return nil, nil
	}
	var aerospikeVectorSearchRoles map[string]interface{}

	err = json.Unmarshal([]byte(aerospikeVectorSearchRolesEnvVariable), &aerospikeVectorSearchRoles)
	if err != nil {
		return nil, err
	}

	if role, ok := aerospikeVectorSearchRoles[label]; ok {
		retval := make(map[string]interface{})
		retval[NODE_ROLES_KEY] = role
		return retval, nil
	}

	return nil, nil
}

func setRoles(aerospikeVectorSearchConfig map[string]interface{}) error {

	roles, err := getAerospikeVectorSearchRoles()
	if err != nil {
		return err
	}

	if roles == nil {
		return nil
	}

	if cluster, ok := aerospikeVectorSearchConfig["cluster"].(map[string]interface{}); ok {
		cluster[NODE_ROLES_KEY] = roles[NODE_ROLES_KEY]
		aerospikeVectorSearchConfig["cluster"] = cluster
	} else {
		return fmt.Errorf("Was not able to get cluster")
	}

	return nil
}

func writeConfig(aerospikeVectorSearchConfig map[string]interface{}) error {
	config, err := yaml.Marshal(aerospikeVectorSearchConfig)
	if err != nil {
		return err
	}

	file, err := os.Create(AVS_CONFIG_FILE_PATH)
	if err != nil {
		return err
	}

	defer func() {
		if err := file.Close(); err != nil {
			panic(err)
		}
	}()
	_, err = file.Write(config)
	if err != nil {
		return err
	}

	return nil
}

func run() int {

	aerospikeVectorSearchConfigEnvVariable := os.Getenv("AEROSPIKE_VECTOR_SEARCH_CONFIG")
	if aerospikeVectorSearchConfigEnvVariable == "" {
		fmt.Println("Aerospike Vector Search configuration is empty")
		return 1
	}

	var aerospikeVectorSearchConfig map[string]interface{}

	err := json.Unmarshal([]byte(aerospikeVectorSearchConfigEnvVariable), &aerospikeVectorSearchConfig)
	if err != nil {
		fmt.Println(err)
		return util.ToExitVal(err)
	}

	err = setRoles(aerospikeVectorSearchConfig)
	if err != nil {
		fmt.Println(err)
		return util.ToExitVal(err)
	}

	err = setAdvertisedListeners(aerospikeVectorSearchConfig)
	if err != nil {
		fmt.Println(err)
		return util.ToExitVal(err)
	}

	err = writeConfig(aerospikeVectorSearchConfig)
	if err != nil {
		fmt.Println(err)
		return util.ToExitVal(err)
	}

	return util.ToExitVal(nil)
}

func main() {
	os.Exit(run())
}
