package main

import (
	"aerospike.com/avs-init-container/v2/util"
	"context"
	"encoding/json"
	"fmt"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"os"
	"sigs.k8s.io/yaml"
)

const (
	AVS_NODE_LABEL_KEY   = "aerospike.io/role-label"
	NODE_ROLES_KEY       = "node-roles"
	AVS_CONFIG_FILE_PATH = "/etc/aerospike-vector-search/aerospike-vector-search.yml"
)

func getNodeLabels() (string, error) {

	nodeName := os.Getenv("NODE_NAME")

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
