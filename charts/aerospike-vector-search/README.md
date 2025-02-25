# Aerospike Vector Search

This Helm chart allows you to configure and run our official [Aerospike Vector Search](https://hub.docker.com/repository/docker/aerospike/aerospike-vector-search)
docker image on a Kubernetes cluster.

This helm chart sets up a `StatefulSet` for each AVS instance. We use a `StatefulSet` instead of a `Deployment`, to have stable DNS names for the  
deployed AVS pods.

## Prerequisites
- Kubernetes cluster
- Helm v3
- An Aerospike cluster that can connect to Pods in the Kubernetes cluster.
  The Aerospike cluster can be deployed in the same Kubernetes cluster using [Aerospike Kubernetes Operator](https://docs.aerospike.com/cloud/kubernetes/operator)
- Ability to deploy a LoadBalancer on K8s in case AVS app runs outside the Kubernetes cluster

## Adding the helm chart repository

Add the `aerospike-helm` helm repository if not already done. (Note: The repository has moved to artifact.aerospike.io. If you are still pointing to aerospike.github.io, please update the repository URL)




```shell
helm repo add aerospike-io https://artifact.aerospike.io/artifactory/api/helm/aerospike-helm

```

## Supported configuration

### Configuration

| **Parameter**                              | **Description**                                                                                                                                                                                                                                                                                           | **Default**                                                                                                                                                                                                                                     |
|--------------------------------------------|-----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|-------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| `replicaCount`                             | Configures the number of AVS instance pods to run.                                                                                                                                                                                                                                                        | `1`                                                                                                                                                                                                                                             |
| `image`                                    | Configures the AVS image repository, tag, and pull policy. See the [values.yaml](values.yaml) for details.                                                                                                                                                                                              | _See values.yaml_                                                                                                                                                                                                                               |
| `imagePullSecrets`                         | For private Docker registries, specify a list of image pull secrets. See the [values.yaml](values.yaml) for details.                                                                                                                                                                                       | _See values.yaml_                                                                                                                                                                                                                               |
| `aerospikeVectorSearchConfig`              | AVS cluster configuration deployed to `/etc/aerospike-vector-search/aerospike-vector-search.yml`. Use this section to define indexing parameters, vector dimensions, port settings, etc.                                                                                                             | _See values.yaml_                                                                                                                                                                                                                               |
| `initContainers`                           | List of additional init containers added to each AVS pod for custom behavior.                                                                                                                                                                                                                             | `[]`                                                                                                                                                                                                                                            |
| `initContainer`                            | Configures the primary init container, which adjusts AVS configuration and node scheduling. Includes image details (repository, tag, pull policy) and can be extended with options such as host networking.                                                                                        | _See values.yaml_ (repository: `"artifact.aerospike.io/container/avs-init-container"`, tag: `""`, pullPolicy: `"IfNotPresent"`)                                                                                                                  |
| `aerospikeVectorSearchNodeRoles`           | Defines a mapping from node pool labels to AVS node roles. For example, nodes labeled as `query-nodes` will be scheduled with the role `query`, and those in the `indexer-nodes` pool with `index-update`. This enables the init container to tailor configuration and pod scheduling.             | [see default](#node-roles-config)                                                                                       |
| `multiPodPerHost`                          | Specifies whether multiple AVS pods can be scheduled on the same host.                                                                                                                                                                                                                                   | `true`                                                                                                                                                                                                                                          |
| `serviceAccount`                           | Service Account details including creation flag, name, and annotations. See the [values.yaml](values.yaml) for further details.                                                                                                                                                                          | _See values.yaml_                                                                                                                                                                                                                               |
| `podAnnotations`                           | Additional pod [annotations](https://kubernetes.io/docs/concepts/overview/working-with-objects/annotations/). Should be specified as a map of annotation names to values.                                                                                                                         | `{}`                                                                                                                                                                                                                                            |
| `podLabels`                                | Additional pod [labels](https://kubernetes.io/docs/concepts/overview/working-with-objects/labels/). Should be specified as a map of label names to values.                                                                                                                                             | `{}`                                                                                                                                                                                                                                            |
| `podSecurityContext`                       | Pod [security context](https://kubernetes.io/docs/tasks/configure-pod-container/security-context/).                                                                                                                                                                                                     | `{}`                                                                                                                                                                                                                                            |
| `securityContext`                          | Container [security context](https://kubernetes.io/docs/tasks/configure-pod-container/security-context/#set-the-security-context-for-a-container).                                                                                                                                                      | `{}`                                                                                                                                                                                                                                            |
| `service`                                  | Load Balancer configuration for AVS. For more details please refer to Load Balancer docs.                                                                                                                                                                                                              | `{}`                                                                                                                                                                                                                                            |
| `resources`                                | Resource [requests and limits](https://kubernetes.io/docs/concepts/configuration/manage-resources-containers/) for the AVS pods.                                                                                                                                                                         | `{}`                                                                                                                                                                                                                                            |
| `autoscaling`                              | Enable the horizontal pod autoscaler. See the [values.yaml](values.yaml) for details.                                                                                                                                                                                                                  | _See values.yaml_                                                                                                                                                                                                                               |
| `extraVolumes`                             | List of additional volumes to attach to the AVS pod. See the [values.yaml](values.yaml) for details.                                                                                                                                                                                                   | _See values.yaml_                                                                                                                                                                                                                               |
| `extraVolumeMounts`                        | Extra volume mounts corresponding to the volumes added in `extraVolumes`. See the [values.yaml](values.yaml) for details.                                                                                                                                                                                 | _See values.yaml_                                                                                                                                                                                                                               |
| `extraSecretVolumeMounts`                  | Extra secret volume mounts corresponding to the volumes added in `extraVolumes`. See the [values.yaml](values.yaml) for details.                                                                                                                                                                          | _See values.yaml_                                                                                                                                                                                                                               |
| `affinity`                                 | [Affinity](https://kubernetes.io/docs/concepts/scheduling-eviction/assign-pod-node/#affinity-and-anti-affinity) rules, if any, for the pods.                                                                                                                                                          | `{}`                                                                                                                                                                                                                                            |
| `nodeSelector`                             | [Node selector](https://kubernetes.io/docs/concepts/scheduling-eviction/assign-pod-node/#nodeselector) for the pods.                                                                                                                                                                                     | `{}`                                                                                                                                                                                                                                            |
| `tolerations`                              | [Tolerations](https://kubernetes.io/docs/concepts/scheduling-eviction/taint-and-toleration/) for the pods.                                                                                                                                                                                             | `{}`                                                                                                                                                                                                                                            |

## Deploy the AVS Cluster

We recommend creating a new `.yaml` for providing configuration values to the helm chart for deployment.
See the [Aerospike Vector](https://github.com/aerospike/aerospike-vector) repository for examples and deployment scripts.

A sample values yaml file for a single node cluster is shown below:

```yaml
aerospikeVectorSearchConfig:
  cluster:
    cluster-name: "avs-db-1"
  feature-key-file: "/etc/aerospike-vector-search/secrets/features.conf"  # Sharing secret with aerospike.

  service:
    ports:
      5000:
        addresses:
          "0.0.0.0"
  manage:
    ports:
      5040: {}
  interconnect:
    ports:
      5001:
        addresses:
          "0.0.0.0"
  aerospike:
    seeds:
      - aerospike-cluster-0-0.aerospike-cluster.aerospike.svc.cluster.local:
          port: 3000
  logging:
    #    file: /var/log/aerospike-vector-search/aerospike-vector-search.log
    enable-console-logging: false
    format: simple
    max-history: 30
    levels:
      metrics-ticker: info
      root: info
    ticker-interval: 10
multiPodPerHost: false
securityContext:
  allowPrivilegeEscalation: false
  runAsUser: 0

service:
  enabled: false
  type: ClusterIP
  ports:
    - name: "svc-port"
      port: 5000
      targetPort: 5000

```

Here `replicaCount` is the count of AVS pods that are deployed.
The AVS configuration is provided as yaml under the key `aerospikeVectorSearchConfig`.

See [Aerospike Vector Search](https://aerospike.com/docs/vector/operate/configuration) configuration documentation for more details on AVS configuration.

### Node Roles Config
Default config is a map of roles. Nodes matching the key will be assigned the corresponding list of roles.

```yaml
aerospikeVectorSearchNodeRoles:
  query-nodes:
    - query
  indexer-nodes:
    - index-update
  standalone-nodes:
    - standalone-indexer
  default-nodes:
    - query
    - index-update
```

### Create a new namespace
We recommend using a namespace for the AVS cluster. If the namespace does not exist run the following command:
```shell
kubectl create namespace avs
```

### Create secrets
Create the secret for avs using your Aerospike licence file
```shell
# kubectl --namespace <target namespace> create secret generic avs-secret--from-file=features.conf=<path to features conf file>
kubectl --namespace avs create secret generic aerospike-secret --from-file=features.conf=features.conf
```

### Deploy the AVS cluster

```shell
# helm install --namespace <target namespace> <helm release name/cluster name> -f <path to custom values yaml> aerospike/aerospike-vector-search
helm install  avs-chart --namespace avs -f avs-values.yaml aerospike/aerospike-vector-search
```

Here `avs` is the release name for the AVS cluster and also its cluster name.

On successful deployment you should see output similar to below:

```shell
NAME: avs
LAST DEPLOYED: Tue May 21 15:55:39 2024
NAMESPACE: avs
STATUS: deployed
REVISION: 1
TEST SUITE: None
NOTES:
```

## List pods for the AVS cluster
To list the pods for the AVS cluster run the following command:
```shell
# kubectl get pods --namespace aerospike --selector=app=<helm release name>-aerospike-vector-search
kubectl get pods --namespace aerospike --selector=app=avs-aerospike-vector-search
```

You should see output similar to the following:
```shell
NAME                            READY   STATUS    RESTARTS   AGE
avs-aerospike-vector-search-0   1/1     Running   0          9m52s
```

If you are using [Aerospike Kubernetes Operator](https://docs.aerospike.com/connect/pulsar/from-asdb/configuring),
see [quote-search](examples/kind) for reference.

## Get logs for all AVS instances

```shell
# kubectl -n aerospike logs -f statefulset/<helm release name>-aerospike-vector-search
# Skip the -f flag to get a one time dump of the log
kubectl -n aerospike logs -f statefulsets/avs-aerospike-vector-search
```

## Get logs for one AVS pod

```shell
# kubectl -n aerospike logs -f <helm release name>-aerospike-vector-search-0
# Skip the -f flag to get a one time dump of the log
kubectl -n aerospike logs -f avs-aerospike-vector-search-0
```

## Updating AVS configuration

Edit the `aerospikeVectorSearchConfig` section in the custom values file and save the changes.

Upgrade the AVS deployment using the following command.

```shell
#helm upgrade --namespace <target namespace> <helm release name> -f <path to custom values yaml file> aerospike/aerospike-vector-search
helm upgrade --namespace avs avs-chart -f avs-values.yaml aerospike/aerospike-vector-search
```

On successful execution of the command the AVS pods will undergo a rolling restart and come up with the new configuration.

To verify the changes are applied
- [List the pods](#list-pods-for-the-avs-cluster)
- [Verify the configuration in AVS logs](#get-logs-for-all-avs-instances)

**_NOTE:_** The changes might take some time to apply. If you do not see the desired AVS config try again after some time.

## Scaling up/down the AVS instances

Edit the `replicaCount` to the desired AVS instance count and upgrade the AVS deployment using the following command.

```shell
#helm upgrade --namespace <target namespace> <helm release name> -f <path to custom values yaml file> aerospike/aerospike-vector-search
helm upgrade --namespace avs avs-chart -f avs-values.yaml aerospike/aerospike-vector-search
```

Verify that the AVS cluster have been scaled.
- [List the pods](#list-pods-for-the-avs-cluster) and verify the count of AVS instances is as desired

**_NOTE:_** The changes might take some time to apply. If you do not see the desired count try again after some time.


