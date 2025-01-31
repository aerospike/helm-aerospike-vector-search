package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"

	"aerospike.com/avs-init-container/v2/util"

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

func init() {
	// Example: Add timestamps, short file name, and line numbers in logs
	log.SetFlags(log.LstdFlags | log.Lshortfile)
}

func getNodeLabels() (string, error) {
	nodeName := os.Getenv("NODE_NAME")
	log.Printf("NODE_NAME environment variable: %s", nodeName)
	if nodeName == "" {
		log.Println("No NODE_NAME environment variable set; skipping node label retrieval")
		return "", nil
	}

	config, err := rest.InClusterConfig()
	if err != nil {
		log.Printf("Error creating in-cluster config: %v", err)
		return "", err
	}
	log.Println("Successfully created in-cluster config")

	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		log.Printf("Error creating Kubernetes clientset: %v", err)
		return "", err
	}
	log.Println("Successfully created Kubernetes clientset")

	node, err := clientset.CoreV1().Nodes().Get(context.TODO(), nodeName, metav1.GetOptions{})
	if err != nil {
		log.Printf("Error retrieving node object: %v", err)
		return "", err
	}
	log.Printf("Retrieved node object %s successfully", nodeName)

	if label, ok := node.Labels[AVS_NODE_LABEL_KEY]; ok {
		log.Printf("Found label '%s' with value '%s'", AVS_NODE_LABEL_KEY, label)
		return label, nil
	}

	log.Printf("Node does not have the '%s' label; returning empty string", AVS_NODE_LABEL_KEY)
	return "", nil
}

func getAerospikeVectorSearchRoles() (map[string]interface{}, error) {
	label, err := getNodeLabels()
	if err != nil {
		log.Printf("Error getting node labels: %v", err)
		return nil, err
	}

	if label == "" {
		log.Println("Node label is empty; no roles to apply")
		return nil, nil
	}

	aerospikeVectorSearchRolesEnvVariable := os.Getenv("AEROSPIKE_VECTOR_SEARCH_NODE_ROLES")
	if aerospikeVectorSearchRolesEnvVariable == "" {
		log.Println("AEROSPIKE_VECTOR_SEARCH_NODE_ROLES environment variable is empty; no roles to apply")
		return nil, nil
	}

	log.Printf("Raw AEROSPIKE_VECTOR_SEARCH_NODE_ROLES: %s", aerospikeVectorSearchRolesEnvVariable)

	var aerospikeVectorSearchRoles map[string]interface{}
	err = json.Unmarshal([]byte(aerospikeVectorSearchRolesEnvVariable), &aerospikeVectorSearchRoles)
	if err != nil {
		log.Printf("Error unmarshaling AEROSPIKE_VECTOR_SEARCH_NODE_ROLES JSON: %v", err)
		return nil, err
	}

	if role, ok := aerospikeVectorSearchRoles[label]; ok {
		log.Printf("Found a matching role for label '%s': %v", label, role)
		retval := make(map[string]interface{})
		retval[NODE_ROLES_KEY] = role
		return retval, nil
	}

	log.Printf("No matching role found for label '%s' in AEROSPIKE_VECTOR_SEARCH_NODE_ROLES", label)
	return nil, nil
}

func setRoles(aerospikeVectorSearchConfig map[string]interface{}) error {
	roles, err := getAerospikeVectorSearchRoles()
	if err != nil {
		log.Printf("Error getting Aerospike Vector Search roles: %v", err)
		return err
	}

	if roles == nil {
		log.Println("No roles found to set; skipping role assignment")
		return nil
	}

	log.Printf("Roles to assign: %v", roles)
	if cluster, ok := aerospikeVectorSearchConfig["cluster"].(map[string]interface{}); ok {
		cluster[NODE_ROLES_KEY] = roles[NODE_ROLES_KEY]
		aerospikeVectorSearchConfig["cluster"] = cluster
		log.Printf("Roles successfully applied to config: %v", aerospikeVectorSearchConfig["cluster"])
	} else {
		errMsg := "Was not able to get 'cluster' key in Aerospike Vector Search config"
		log.Println(errMsg)
		return fmt.Errorf(errMsg)
	}

	return nil
}

func writeConfig(aerospikeVectorSearchConfig map[string]interface{}) error {
	configYAML, err := yaml.Marshal(aerospikeVectorSearchConfig)
	if err != nil {
		log.Printf("Error converting config to YAML: %v", err)
		return err
	}
	log.Printf("Writing the following YAML to %s:\n%s", AVS_CONFIG_FILE_PATH, string(configYAML))

	file, err := os.Create(AVS_CONFIG_FILE_PATH)
	if err != nil {
		log.Printf("Error creating config file '%s': %v", AVS_CONFIG_FILE_PATH, err)
		return err
	}
	defer func() {
		if cerr := file.Close(); cerr != nil {
			log.Printf("Error closing file '%s': %v", AVS_CONFIG_FILE_PATH, cerr)
		}
	}()

	_, err = file.Write(configYAML)
	if err != nil {
		log.Printf("Error writing config YAML to file: %v", err)
		return err
	}

	log.Printf("Successfully wrote config to file: %s", AVS_CONFIG_FILE_PATH)
	return nil
}

func run() int {
	aerospikeVectorSearchConfigEnvVariable := os.Getenv("AEROSPIKE_VECTOR_SEARCH_CONFIG")
	if aerospikeVectorSearchConfigEnvVariable == "" {
		log.Println("AEROSPIKE_VECTOR_SEARCH_CONFIG environment variable is empty. Nothing to do.")
		return 1
	}
	log.Printf("Raw AEROSPIKE_VECTOR_SEARCH_CONFIG: %s", aerospikeVectorSearchConfigEnvVariable)

	var aerospikeVectorSearchConfig map[string]interface{}
	err := json.Unmarshal([]byte(aerospikeVectorSearchConfigEnvVariable), &aerospikeVectorSearchConfig)
	if err != nil {
		log.Printf("Error unmarshaling AEROSPIKE_VECTOR_SEARCH_CONFIG: %v", err)
		return util.ToExitVal(err)
	}
	log.Printf("Successfully unmarshaled Aerospike Vector Search config: %v", aerospikeVectorSearchConfig)

	err = setRoles(aerospikeVectorSearchConfig)
	if err != nil {
		log.Printf("Error setting roles: %v", err)
		return util.ToExitVal(err)
	}

	err = writeConfig(aerospikeVectorSearchConfig)
	if err != nil {
		log.Printf("Error writing config: %v", err)
		return util.ToExitVal(err)
	}

	log.Println("Init container operations completed successfully")
	return util.ToExitVal(nil)
}

func main() {
	os.Exit(run())
}
