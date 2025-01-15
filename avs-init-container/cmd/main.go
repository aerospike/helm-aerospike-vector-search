package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"

	"aerospike.com/avs-init-container/v2/util"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/yaml"
)

const (
	NODE_ROLES_KEY       = "node-roles"
	AVS_NODE_LABEL_KEY   = "aerospike.io/role-label"
	AVS_CONFIG_FILE_PATH = "/etc/aerospike-vector-search/aerospike-vector-search.yml"
)

func getNodeLabels() (string, error) {

	nodeName := os.Getenv("NODE_NAME")
	if nodeName == "" {
		return "", fmt.Errorf("NODE_NAME environment variable not set")
	}

	config, err := rest.InClusterConfig()
	if err != nil {
		return "", err
	}

	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return "", err
	}

	node, err := clientset.CoreV1().Nodes().Get(context.TODO(), nodeName, metav1.GetOptions{})
	if err != nil {
		return "", err
	}

	if label, ok := node.Labels[AVS_NODE_LABEL_KEY]; ok {
		return label, nil
	}
	return "", nil
}

func getAerospikeVectorSearchNodeRoles() (map[string]interface{}, error) {

	label, err := getNodeLabels()
	if err != nil {
		return nil, err
	}

	if label == "" {
		return nil, nil
	}

	aerospikeVectorSearchNodeRolesEnvVariable := os.Getenv("AEROSPIKE_VECTOR_SEARCH_NODE_ROLES")
	if aerospikeVectorSearchNodeRolesEnvVariable == "" {
		return nil, nil
	}
	var aerospikeVectorSearchNodeRoles map[string]interface{}

	err = json.Unmarshal([]byte(aerospikeVectorSearchNodeRolesEnvVariable), &aerospikeVectorSearchNodeRoles)
	if err != nil {
		return nil, err
	}

	if role, ok := aerospikeVectorSearchNodeRoles[label]; ok {
		retval := make(map[string]interface{})
		retval[NODE_ROLES_KEY] = role
		return retval, nil
	}

	return nil, nil
}

func setNodeRoles(aerospikeVectorSearchConfig map[string]interface{}) error {

	nodeRoles, err := getAerospikeVectorSearchNodeRoles()
	if err != nil {
		return err
	}

	if nodeRoles == nil {
		return nil
	}

	if cluster, ok := aerospikeVectorSearchConfig["cluster"].(map[string]interface{}); ok {
		cluster[NODE_ROLES_KEY] = nodeRoles[NODE_ROLES_KEY]
		aerospikeVectorSearchConfig["cluster"] = cluster
	} else {
		return fmt.Errorf("Was not able to get cluster")
	}

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

	err = setNodeRoles(aerospikeVectorSearchConfig)
	if err != nil {
		fmt.Println(err)
		return util.ToExitVal(err)
	}

	err = setHeartbeatSeeds(aerospikeVectorSearchConfig)
	if err != nil {
		fmt.Println(err)
		return util.ToExitVal(err)
	}

	err = writeConfigFile(aerospikeVectorSearchConfig)
	if err != nil {
		fmt.Println(err)
		return util.ToExitVal(err)
	}

	return util.ToExitVal(nil)
}

func main() {

	os.Exit(run())
}
