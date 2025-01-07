package main

import (
	"aerospike.com/avs-init-container/v2/util"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
)

const NODE_ROLES_KEY string = "node-roles"

func getAerospikeVectorSearchRoles() (map[string], error) {

	aerospikeVectorSearchRolesEnvVariable := os.Getenv("AEROSPIKE_VECTOR_SERACH_ROLES")
	if aerospikeVectorSearchRolesEnvVariable == "" {
		return nil, nil
	}

	var aerospikeVectorSearchRoles map[string]interface{}

	err := json.Unmarshal([]byte(aerospikeVectorSearchRolesEnvVariable), &aerospikeVectorSearchRoles)
	if err != nil {
		return nil, err
	}

	pod_name := os.Getenv("POD_NAME")
	if pod_name == "" {
		return nil, fmt.Errorf("%s", "POD_NAME env variable is empty")
	}

	re := regexp.MustCompile(`\d+$`)
	match := re.FindString(pod_name)
	if match == "" {
		return nil, fmt.Errorf("%s", "POD_NAME env variable has bad format")
	}

	if role, ok := aerospikeVectorSearchRoles[fmt.Sprintf("node-%s", match)]; ok {
		retval := make(map[string]interface{})
		retval[NODE_ROLES_KEY] = role
		return retval, nil
	}

	return nil, nil

}

func run() int {

	return util.ToExitVal(nil)
}

func main() {
	os.Exit(run())
}
