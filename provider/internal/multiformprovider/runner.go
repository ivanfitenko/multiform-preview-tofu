package multiformprovider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// runResult captures a completed subprocess's output.
type runResult struct {
	Stdout string
	Stderr string
}

// envVarsToStrip are environment variables that configure the *outer*
// Terraform/OpenTofu's own behavior and must not leak into the inner
// multiform subprocess, which has entirely separate CLI-config needs
// (e.g. its own provider mirror, possibly for a platform the outer run
// doesn't even need one for). Discovered the hard way: testing this
// provider under dev_overrides (which requires a CLI config file
// pointed at via TF_CLI_CONFIG_FILE) broke the inner run's own provider
// resolution, because that same env var leaked through and replaced
// the inner tofu's default ~/.tofurc entirely instead of adding to it.
var envVarsToStrip = []string{"TF_CLI_CONFIG_FILE"}

// run executes binaryPath with args in workDir, with extraEnv (e.g.
// MULTIFORM_GLOBAL_STATE_FILE) added on top. Otherwise inherits the
// parent process's environment unchanged - this is how credentials
// (AWS_ACCESS_KEY_ID, etc.) already reach the inner multiform run today,
// with no special handling needed for those.
func run(ctx context.Context, binaryPath, workDir string, extraEnv map[string]string, args ...string) (runResult, error) {
	cmd := exec.CommandContext(ctx, binaryPath, args...)
	cmd.Dir = workDir
	env := filterEnv(os.Environ(), envVarsToStrip)
	for k, v := range extraEnv {
		env = append(env, k+"="+v)
	}
	cmd.Env = env

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	result := runResult{Stdout: stdout.String(), Stderr: stderr.String()}
	if err != nil {
		return result, fmt.Errorf("%s %v: %w\n%s", binaryPath, args, err, result.Stderr)
	}
	return result, nil
}

// filterEnv returns env with every "KEY=value" entry whose key is in
// exclude removed.
func filterEnv(env []string, exclude []string) []string {
	filtered := make([]string, 0, len(env))
	for _, kv := range env {
		key, _, _ := strings.Cut(kv, "=")
		skip := false
		for _, e := range exclude {
			if key == e {
				skip = true
				break
			}
		}
		if !skip {
			filtered = append(filtered, kv)
		}
	}
	return filtered
}

// planResourceChange is the minimal shape of a `tofu show -json`
// resource_changes entry that we need - just enough to tell a real
// add/update/delete/replace apart from a no-op/read.
type planResourceChange struct {
	Change struct {
		Actions []string `json:"actions"`
	} `json:"change"`
}

type planJSON struct {
	ResourceChanges []planResourceChange `json:"resource_changes"`
}

// countRealChanges parses `tofu show -json <planfile>` output and counts
// resource changes that are neither a no-op nor a plain read.
func countRealChanges(jsonOut []byte) (int, error) {
	var parsed planJSON
	if err := json.Unmarshal(jsonOut, &parsed); err != nil {
		return 0, fmt.Errorf("parsing plan JSON: %w", err)
	}
	count := 0
	for _, rc := range parsed.ResourceChanges {
		if !isNoopActions(rc.Change.Actions) {
			count++
		}
	}
	return count, nil
}

func isNoopActions(actions []string) bool {
	if len(actions) == 0 {
		return true
	}
	return len(actions) == 1 && (actions[0] == "no-op" || actions[0] == "read")
}
