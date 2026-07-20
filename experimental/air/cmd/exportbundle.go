package aircmd

import (
	"fmt"
	"maps"
	"path/filepath"
	"strings"

	"go.yaml.in/yaml/v3"
)

// This file converts a train.yaml runConfig to a databricks.yml Asset Bundle
// deploying the same workload as a native ai_runtime_task. It backs both
// `air export-bundle` and the `air run` submit path (rundabs.go).
//
// checkBundleConvertible rejects configs a bundle cannot represent faithfully:
// train.yaml and a bundle are not 1:1, and some run fields have no bundle
// equivalent (e.g. docker_image, usage_policy, a git-pinned code_source).

// bundleCommandScript is the entrypoint filename the emitted bundle references and
// that `bundle sync` uploads alongside the user's code.
const bundleCommandScript = "command.sh"

// aiRuntimeEnvVarsKey links the task's environment_variables_key to the single
// job-level env-var profile the converter emits.
const aiRuntimeEnvVarsKey = "default"

// checkBundleConvertible reports why a structurally-valid runConfig cannot be
// converted to a faithful bundle, or nil if it can. Each reason names the source
// field so the CLI can reject with an actionable message rather than emit a lossy
// databricks.yml. env_variables/secrets are representable (see envVarProfiles) and
// are not rejected here.
func checkBundleConvertible(cfg *runConfig) error {
	var reasons []string

	// docker_image: a custom image must be registered before it can be referenced;
	// that registration is not part of a bundle deploy.
	if cfg.dockerImageURL() != "" {
		reasons = append(reasons, "environment.docker_image: needs image registration, which a bundle deploy does not perform")
	}

	// usage_policy_*: the name/id resolves to a budget policy via a workspace
	// lookup at submit time; a static bundle has nowhere to run that lookup.
	if cfg.UsagePolicyName != nil {
		reasons = append(reasons, "usage_policy_name: resolved by a workspace lookup at submit time, not representable statically")
	}
	if cfg.UsagePolicyID != nil {
		reasons = append(reasons, "usage_policy_id: budget policy binding is not represented on the ai_runtime_task bundle path yet")
	}

	// The aicode mutator tarballs the local working tree to Workspace Files; a git
	// pin or a Volume destination can't be represented that way.
	if cfg.CodeSource != nil && cfg.CodeSource.Snapshot != nil {
		snap := cfg.CodeSource.Snapshot
		if snap.Git != nil {
			reasons = append(reasons, "code_source.snapshot.git: the code snapshot tarballs the working tree and cannot pin a git commit or fetch a remote branch")
		}
		if snap.RemoteVolume != nil {
			reasons = append(reasons, "code_source.snapshot.remote_volume: the code snapshot uploads to Workspace Files, not a UC Volume")
		}
	}

	if len(reasons) == 0 {
		return nil
	}
	return fmt.Errorf(
		"this train.yaml cannot be converted to a faithful bundle:\n  - %s\nrun it with `air run` until these are supported on the bundle path",
		strings.Join(reasons, "\n  - "))
}

// exportedBundle is the minimal databricks.yml shape the converter emits: a bundle
// name plus one job with a single ai_runtime_task. It marshals to YAML, so field
// order here is the emitted key order.
type exportedBundle struct {
	Bundle    bundleBlock            `yaml:"bundle"`
	Resources exportedResourcesBlock `yaml:"resources"`
}

type bundleBlock struct {
	Name string `yaml:"name"`
}

type exportedResourcesBlock struct {
	Jobs map[string]exportedJob `yaml:"jobs"`
}

type exportedJob struct {
	Name         string                `yaml:"name"`
	Tasks        []exportedTask        `yaml:"tasks"`
	Environments []exportedEnvironment `yaml:"environments"`
	// EnvironmentVariables holds env-var profiles, referenced by a task's
	// EnvironmentVariablesKey. Emitted only when the run declares env_variables or
	// secrets.
	EnvironmentVariables []exportedEnvVarProfile `yaml:"environment_variables,omitempty"`
	// Permissions are the job's ACL grants, from the run's permissions block.
	// Emitted only when the run declares any.
	Permissions []exportedPermission `yaml:"permissions,omitempty"`
}

// exportedPermission is one job ACL grant: a level plus exactly one principal.
// Matches the DABs job permissions shape.
type exportedPermission struct {
	Level                string `yaml:"level"`
	UserName             string `yaml:"user_name,omitempty"`
	GroupName            string `yaml:"group_name,omitempty"`
	ServicePrincipalName string `yaml:"service_principal_name,omitempty"`
}

type exportedTask struct {
	TaskKey        string `yaml:"task_key"`
	EnvironmentKey string `yaml:"environment_key"`
	// EnvironmentVariablesKey references a job-level environment_variables profile.
	// Omitted when the run has no env vars or secrets.
	EnvironmentVariablesKey string                `yaml:"environment_variables_key,omitempty"`
	MaxRetries              int                   `yaml:"max_retries"`
	TimeoutSeconds          int                   `yaml:"timeout_seconds,omitempty"`
	AiRuntimeTask           exportedAiRuntimeTask `yaml:"ai_runtime_task"`
}

// exportedEnvVarProfile is one entry in the job-level environment_variables list.
// Variables holds plain values inline and secrets as {{secrets/scope/key}}
// references, resolved by Jobs at run time.
type exportedEnvVarProfile struct {
	EnvironmentVariablesKey string            `yaml:"environment_variables_key"`
	Variables               map[string]string `yaml:"variables"`
}

type exportedAiRuntimeTask struct {
	Experiment                string `yaml:"experiment"`
	MlflowRun                 string `yaml:"mlflow_run,omitempty"`
	MlflowExperimentDirectory string `yaml:"mlflow_experiment_directory,omitempty"`
	// CodeSourcePath is the local code directory (relative to the bundle root); the
	// aicode mutator tarballs it and rewrites this to the remote path. Omitted when
	// the run has no code_source.
	CodeSourcePath string               `yaml:"code_source_path,omitempty"`
	Deployments    []exportedDeployment `yaml:"deployments"`
}

type exportedDeployment struct {
	Name        string          `yaml:"name"`
	CommandPath string          `yaml:"command_path"`
	Compute     exportedCompute `yaml:"compute"`
}

type exportedCompute struct {
	AcceleratorType  string `yaml:"accelerator_type"`
	AcceleratorCount int    `yaml:"accelerator_count"`
}

type exportedEnvironment struct {
	EnvironmentKey string          `yaml:"environment_key"`
	Spec           exportedEnvSpec `yaml:"spec"`
}

// exportedEnvSpec carries the serverless runtime selection. The two fields are
// mutually exclusive (the Jobs serverless validator rejects setting both): a bare
// numeric channel uses environment_version; the databricks-ai managed environment
// (torch + ML venv preinstalled) uses base_environment. omitempty on both so only
// the resolved one is emitted.
type exportedEnvSpec struct {
	EnvironmentVersion string `yaml:"environment_version,omitempty"`
	BaseEnvironment    string `yaml:"base_environment,omitempty"`
}

// databricksAITokenPrefix marks an environment.version that selects the
// databricks-ai managed base environment rather than a bare channel.
const databricksAITokenPrefix = "databricks_ai_v"

// databricksAIBaseEnvironment is the system base_environment id the databricks-ai
// token resolves to.
const databricksAIBaseEnvironment = "workspace-base-environments/"

// convertToBundle maps a convertible runConfig to the emitted bundle, assuming
// checkBundleConvertible has already passed. command_path is emitted as a path
// relative to the bundle root; the bundle's translate_paths mutator rewrites it to
// the deployed (synced) location. Emitting ${workspace.file_path} directly would
// instead be validated as a local file and fail.
func convertToBundle(cfg *runConfig) *exportedBundle {
	task := exportedAiRuntimeTask{
		Experiment: cfg.ExperimentName,
		Deployments: []exportedDeployment{{
			// The deployment name is cosmetic (the wire payload carries none).
			Name:        "worker",
			CommandPath: bundleCommandScript,
			Compute: exportedCompute{
				AcceleratorType:  cfg.Compute.AcceleratorType,
				AcceleratorCount: cfg.Compute.NumAccelerators,
			},
		}},
	}
	// Point code_source_path at the staged code subdir; the aicode mutator packages
	// it and rewrites this to the remote path.
	if dir := codeSourceDirName(cfg); dir != "" {
		task.CodeSourcePath = "./" + dir
	}
	if cfg.MLflowRunName != nil {
		task.MlflowRun = *cfg.MLflowRunName
	}
	if cfg.MLflowExperimentDirectory != nil {
		task.MlflowExperimentDirectory = *cfg.MLflowExperimentDirectory
	}

	tsk := exportedTask{
		TaskKey:        cfg.ExperimentName,
		EnvironmentKey: aiRuntimeEnvironmentKey,
		MaxRetries:     cfg.maxRetries(),
		TimeoutSeconds: cfg.timeoutSeconds(),
		AiRuntimeTask:  task,
	}

	// One job-level env-var profile, referenced by the task's
	// environment_variables_key. Emitted only when there are vars or secrets.
	profiles := envVarProfiles(cfg)
	if len(profiles) > 0 {
		tsk.EnvironmentVariablesKey = aiRuntimeEnvVarsKey
	}

	// The experiment name is safe as a resource key: validateExperimentName already
	// guarantees the task_key charset.
	return &exportedBundle{
		Bundle: bundleBlock{Name: cfg.ExperimentName},
		Resources: exportedResourcesBlock{
			Jobs: map[string]exportedJob{
				cfg.ExperimentName: {
					Name:  cfg.ExperimentName,
					Tasks: []exportedTask{tsk},
					Environments: []exportedEnvironment{{
						EnvironmentKey: aiRuntimeEnvironmentKey,
						Spec:           exportBundleEnvSpec(cfg),
					}},
					EnvironmentVariables: profiles,
					Permissions:          exportedPermissions(cfg),
				},
			},
		},
	}
}

// codeSourceDirName returns the code subdir name (the tarball prefix), or "" when
// the run has no code_source.
func codeSourceDirName(cfg *runConfig) string {
	if cfg.CodeSource == nil || cfg.CodeSource.Snapshot == nil {
		return ""
	}
	return dirNameForRoot(cfg.CodeSource.Snapshot.RootPath)
}

// dirNameForRoot is the basename of root_path, falling back to "code" for ".", "/"
// or "" so the code is always staged in its own subdir, never the bundle root.
func dirNameForRoot(rootPath string) string {
	base := filepath.Base(strings.TrimRight(rootPath, "/"))
	if base == "." || base == "/" || base == "" {
		return "code"
	}
	return base
}

// exportedPermissions maps the run's permissions block to job ACL grants, or nil
// when none are declared. Each entry carries the level and its single principal;
// validation (exactly one principal, non-empty level) already ran in runConfig.
func exportedPermissions(cfg *runConfig) []exportedPermission {
	if len(cfg.Permissions) == 0 {
		return nil
	}
	out := make([]exportedPermission, 0, len(cfg.Permissions))
	for _, p := range cfg.Permissions {
		e := exportedPermission{Level: p.Level}
		switch {
		case p.UserName != nil:
			e.UserName = *p.UserName
		case p.GroupName != nil:
			e.GroupName = *p.GroupName
		case p.ServicePrincipalName != nil:
			e.ServicePrincipalName = *p.ServicePrincipalName
		}
		out = append(out, e)
	}
	return out
}

// envVarProfiles builds the job-level env-var profile list from the run's
// env_variables and secrets, or nil when there are none. Plain values are inline;
// each secret (ENV_VAR -> "scope/key") becomes a {{secrets/scope/key}} reference
// Jobs resolves at run time.
func envVarProfiles(cfg *runConfig) []exportedEnvVarProfile {
	if len(cfg.EnvVariables) == 0 && len(cfg.Secrets) == 0 {
		return nil
	}
	variables := make(map[string]string, len(cfg.EnvVariables)+len(cfg.Secrets))
	maps.Copy(variables, cfg.EnvVariables)
	for envVar, secretRef := range cfg.Secrets {
		variables[envVar] = "{{secrets/" + secretRef + "}}"
	}
	return []exportedEnvVarProfile{{
		EnvironmentVariablesKey: aiRuntimeEnvVarsKey,
		Variables:               variables,
	}}
}

// exportBundleEnvSpec resolves the serverless runtime selection: a
// "databricks_ai_v<N>" token selects the managed databricks-ai base_environment
// (torch + ML venv), while a bare numeric channel ("4", "5", ...) uses
// environment_version. It does not read process env, so the generated bundle is
// reproducible.
func exportBundleEnvSpec(cfg *runConfig) exportedEnvSpec {
	channel := strings.TrimPrefix(defaultDlRuntimeImage, "CLIENT-GPU-")
	if v, ok := cfg.runtimeVersion(); ok {
		channel = strings.TrimPrefix(v, "CLIENT-GPU-")
	}
	if strings.HasPrefix(channel, databricksAITokenPrefix) {
		return exportedEnvSpec{BaseEnvironment: databricksAIBaseEnvironment + channel}
	}
	return exportedEnvSpec{EnvironmentVersion: channel}
}

// marshalBundle renders the bundle to YAML with a header explaining provenance and
// the steps the user must complete before deploying (add a targets block; the code
// and command.sh are synced from the bundle folder).
func marshalBundle(b *exportedBundle, sourcePath string) ([]byte, error) {
	body, err := yaml.Marshal(b)
	if err != nil {
		return nil, err
	}
	header := "# Generated by `air export-bundle` from " + filepath.Base(sourcePath) + ".\n" +
		"#\n" +
		"# Deploys the same workload as a durable Jobs resource. `bundle deploy` syncs this\n" +
		"# folder (including " + bundleCommandScript + " and your code) to the workspace; the task's\n" +
		"# command_path points at the synced " + bundleCommandScript + ". Before deploying, add a\n" +
		"# `targets` block with your workspace host. See the ai-compute DABs examples.\n"
	return append([]byte(header), body...), nil
}
