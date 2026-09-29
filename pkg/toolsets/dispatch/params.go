// Package dispatch defines the shared types of the merged (enum-dispatched)
// tools: rancherQuery, diagnose, planChange and executeChange. It is a leaf
// package: it must never import any toolset package.
package dispatch

import "encoding/json"

// QueryParams is the flat parameter set of the rancherQuery tool. Only
// Resource is schema-required; every other field is required only for the
// resource values listed in its description and enforced at runtime by
// Validate, which returns an error naming the missing fields.
type QueryParams struct {
	Resource     string   `json:"resource" jsonschema:"required. Which Rancher resource to query: project|projects|resourceUsage|clusters|user|roleTemplate|roleTemplates|clusterRTBs|projectRTBs|gitRepo|gitRepos|bundle|clusterImages|k3kClusters|clusterMachine|supportedVersions"`
	Cluster      string   `json:"cluster,omitempty" jsonschema:"cluster name or ID. Required by: project, projects, resourceUsage, clusterRTBs, projectRTBs, clusterMachine"`
	Name         string   `json:"name,omitempty" jsonschema:"object identifier. Required by: project (project name), user (username), roleTemplate, gitRepo, bundle, clusterMachine (machine name)"`
	Workspace    string   `json:"workspace,omitempty" jsonschema:"Fleet workspace. Required by: gitRepo, gitRepos, bundle"`
	Namespace    string   `json:"namespace,omitempty" jsonschema:"namespace filter. Optional for: resourceUsage"`
	Project      string   `json:"project,omitempty" jsonschema:"project filter. Optional for: resourceUsage (name or ID), projectRTBs (project ID)"`
	User         string   `json:"user,omitempty" jsonschema:"user ID filter. Optional for: clusterRTBs, projectRTBs"`
	Group        string   `json:"group,omitempty" jsonschema:"group filter. Optional for: clusterRTBs, projectRTBs"`
	Clusters     []string `json:"clusters,omitempty" jsonschema:"cluster name filter list. Optional for: clusterImages, k3kClusters (empty = all clusters)"`
	Distribution string   `json:"distribution,omitempty" jsonschema:"kubernetes distribution: rke2 or k3s. Required by: supportedVersions"`
}

// DiagnoseParams is the flat parameter set of the diagnose tool.
type DiagnoseParams struct {
	Target    string `json:"target" jsonschema:"required. What to diagnose: cluster|machines|nodes|fleet|deployment|pod"`
	Cluster   string `json:"cluster,omitempty" jsonschema:"cluster name or ID. Required by: cluster, machines, nodes, deployment, pod"`
	Namespace string `json:"namespace,omitempty" jsonschema:"namespace. Optional for: cluster, machines. Required by: deployment, pod"`
	Name      string `json:"name,omitempty" jsonschema:"object name. Required by: deployment, pod"`
	Workspace string `json:"workspace,omitempty" jsonschema:"Fleet workspace. Required by: fleet"`
}

// K3kSync mirrors provisioning.SyncConfig for the createK3kCluster operation.
type K3kSync struct {
	PriorityClasses bool `json:"priorityClasses,omitempty" jsonschema:"sync priorityClasses"`
	Ingresses       bool `json:"ingresses,omitempty" jsonschema:"sync ingresses"`
}

// K3kPersistence mirrors provisioning.PersistenceConfig.
type K3kPersistence struct {
	Type             string `json:"type,omitempty" jsonschema:"persistence type, e.g. pvc or ephemeral"`
	StorageClassName string `json:"storageClassName,omitempty" jsonschema:"storage class to use for the PVC"`
	StorageRequest   string `json:"storageRequest,omitempty" jsonschema:"storage request size, e.g. 5Gi"`
}

// K3kLimits mirrors provisioning.ResourceLimits.
type K3kLimits struct {
	CPU    string `json:"cpu,omitempty" jsonschema:"CPU limit, e.g. 1 or 500m"`
	Memory string `json:"memory,omitempty" jsonschema:"memory limit, e.g. 2Gi or 512Mi"`
}

// ChangeParams is the flat parameter set of the planChange and executeChange
// tools. Only Operation is schema-required (plus ConfirmationToken for
// executeChange); per-operation requirements are enforced by Validate.
type ChangeParams struct {
	Operation         string `json:"operation" jsonschema:"required. Which change: createKubernetesResource|patchKubernetesResource|deleteKubernetesResource|scaleClusterNodePool|execPod|createProject|moveNamespace|createCustomCluster|createImportedCluster|createK3kCluster"`
	Cluster           string `json:"cluster,omitempty" jsonschema:"cluster name or ID. Required by all operations except createCustomCluster, createImportedCluster"`
	Namespace         string `json:"namespace,omitempty" jsonschema:"namespace (empty for cluster-wide resources). Required by: scaleClusterNodePool, execPod, moveNamespace; optional for: createKubernetesResource, patchKubernetesResource, deleteKubernetesResource, createK3kCluster (k3k namespace)"`
	Name              string `json:"name,omitempty" jsonschema:"object name. Required by every operation except none; for execPod it is the pod name"`
	Description       string `json:"description,omitempty" jsonschema:"optional human description. Used by: createProject, createCustomCluster, createImportedCluster"`
	ConfirmationToken string `json:"confirmationToken,omitempty" jsonschema:"REQUIRED by executeChange (unless the server runs in auto-write mode): the single-use confirmationToken returned by planChange for THIS exact operation and parameters. Never invent, reuse, or guess a token"`
	// k8s-generic operations
	Kind       string          `json:"kind,omitempty" jsonschema:"Kubernetes resource kind (custom resources supported). Required by: createKubernetesResource, patchKubernetesResource, deleteKubernetesResource"`
	APIVersion string          `json:"apiVersion,omitempty" jsonschema:"optional API group and version (e.g. harvesterhci.io/v1beta1) to disambiguate custom resources"`
	Manifest   string          `json:"manifest,omitempty" jsonschema:"complete Kubernetes manifest in YAML or JSON. Required by: createKubernetesResource"`
	Patch      json.RawMessage `json:"patch,omitempty" jsonschema:"RFC 6902 JSON patch array. Required by: patchKubernetesResource. Example: [{\"op\":\"replace\",\"path\":\"/spec/replicas\",\"value\":3}]"`
	// provisioning cluster operations
	CNI                      string         `json:"CNI,omitempty" jsonschema:"CNI to use. Required by: createCustomCluster"`
	Version                  string         `json:"version,omitempty" jsonschema:"rke2/k3s version. Required by: createCustomCluster; optional for: createK3kCluster"`
	Distribution             string         `json:"distribution,omitempty" jsonschema:"rke2 or k3s. Required by: createCustomCluster"`
	VersionManagementSetting string         `json:"VersionManagementSetting,omitempty" jsonschema:"version management setting: system-default, true or false. Optional for: createImportedCluster"`
	TargetCluster            string         `json:"targetCluster,omitempty" jsonschema:"downstream cluster hosting the K3k cluster. Required by: createK3kCluster"`
	Mode                     string         `json:"mode,omitempty" jsonschema:"k3k mode: shared or virtual. Optional for: createK3kCluster"`
	Servers                  int32          `json:"servers,omitempty" jsonschema:"number of k3k server (control plane) nodes. Optional for: createK3kCluster"`
	Agents                   int32          `json:"agents,omitempty" jsonschema:"number of k3k agent (worker) nodes. Optional for: createK3kCluster"`
	Sync                     K3kSync        `json:"sync,omitempty" jsonschema:"k3k shared-mode sync options. Optional for: createK3kCluster"`
	Persistence              K3kPersistence `json:"persistence,omitempty" jsonschema:"k3k etcd persistence. Optional for: createK3kCluster"`
	ServerLimit              K3kLimits      `json:"serverLimit,omitempty" jsonschema:"k3k server resource limits. Optional for: createK3kCluster"`
	WorkerLimit              K3kLimits      `json:"workerLimit,omitempty" jsonschema:"k3k worker resource limits. Optional for: createK3kCluster"`
	// createProject quotas
	DisplayName       string `json:"displayName,omitempty" jsonschema:"project display name. Optional for: createProject"`
	CPULimit          int    `json:"cpuLimit,omitempty" jsonschema:"max CPU (mCPUs) for containers in the project. Optional for: createProject"`
	CPUReservation    int    `json:"cpuReservation,omitempty" jsonschema:"reserved CPU (mCPUs). Optional for: createProject"`
	MemoryLimit       int    `json:"memoryLimit,omitempty" jsonschema:"max memory (MiB). Optional for: createProject"`
	MemoryReservation int    `json:"memoryReservation,omitempty" jsonschema:"reserved memory (MiB). Optional for: createProject"`
	// moveNamespace
	Project string `json:"project,omitempty" jsonschema:"destination project name or ID. Required by: moveNamespace"`
	// scaleClusterNodePool
	NodePoolName     string `json:"nodePoolName,omitempty" jsonschema:"the node pool to scale. Required by: scaleClusterNodePool"`
	DesiredSize      int    `json:"desiredSize,omitempty" jsonschema:"target pool size; ignored when amountToAdd/amountToSubtract is set. Optional for: scaleClusterNodePool"`
	AmountToAdd      int    `json:"amountToAdd,omitempty" jsonschema:"nodes to add. Optional for: scaleClusterNodePool"`
	AmountToSubtract int    `json:"amountToSubtract,omitempty" jsonschema:"nodes to remove. Optional for: scaleClusterNodePool"`
	// execPod
	Container string   `json:"container,omitempty" jsonschema:"container to execute in. Optional for: execPod (defaults to the first container)"`
	Command   []string `json:"command,omitempty" jsonschema:"argv array to execute, e.g. [\"ls\",\"-la\"]. Required by: execPod. Never wrap in a shell unless the user explicitly asked"`
}
