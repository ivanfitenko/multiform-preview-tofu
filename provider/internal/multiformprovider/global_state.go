package multiformprovider

import (
	"encoding/base64"
	"encoding/json"
	"os"
)

// globalStatePrivateKey stashes the "global"/primary role's state bytes
// between calls, the same way planPrivateKey stashes a computed plan -
// see globalStateFileEnvVar's own doc comment in
// internal/backend/local/multiform_state.go for why: a separate,
// independently-configured backend.tf-declared state file living only
// inside root_dir would be a second artifact that could drift out of
// sync with the outer Terraform project's own state for this resource,
// particularly for `state pull`/`state push` against the outer project.
const globalStatePrivateKey = "globalstate"

// globalStateFileEnvVar must match multiform's own
// internal/backend/local/multiform_state.go's globalStateFileEnvVar
// constant exactly - duplicated here as a literal since that's an
// internal/ package in a different Go module and so isn't importable
// from here.
const globalStateFileEnvVar = "MULTIFORM_GLOBAL_STATE_FILE"

// stageGlobalState decodes a stashed global-state blob (if any, in the
// same base64-inside-JSON-string shape used for the plan stash) and
// writes it to a fresh temporary file, returning that file's path for use
// as MULTIFORM_GLOBAL_STATE_FILE. If there's no stashed blob yet
// (first-ever run for this resource), no file is left on disk at that
// path - multiform treats a nonexistent path as a fresh/empty state,
// exactly as a brand new backend.tf-declared backend would. Callers must
// call the returned cleanup func when done.
func stageGlobalState(encoded []byte) (path string, cleanup func(), err error) {
	f, err := os.CreateTemp("", "multiform-global-state-*.tfstate")
	if err != nil {
		return "", nil, err
	}
	path = f.Name()
	f.Close()
	cleanup = func() { os.Remove(path) }

	if len(encoded) == 0 {
		os.Remove(path)
		return path, cleanup, nil
	}

	var b64 string
	if err := json.Unmarshal(encoded, &b64); err != nil {
		cleanup()
		return "", nil, err
	}
	decoded, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		cleanup()
		return "", nil, err
	}
	if err := os.WriteFile(path, decoded, 0o600); err != nil {
		cleanup()
		return "", nil, err
	}
	return path, cleanup, nil
}

// collectGlobalState reads back the (possibly updated, possibly still
// nonexistent) file at path and returns it re-encoded for stashing via
// Private.SetKey, in the same shape stageGlobalState reads. Returns a nil
// slice (not an error) if the file was never created - nothing changed to
// stash.
func collectGlobalState(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	return json.Marshal(base64.StdEncoding.EncodeToString(data))
}
