package alo

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type VerifyOutcome int

const (
	VerifyInfrastructureFailure VerifyOutcome = iota
	VerifyCandidateFailure
	VerifySuccess
)

type VerifyResult struct {
	Outcome  VerifyOutcome
	LogPath  string
	Failure  string
	Outputs  map[string]OutputRecord
	ExitCode int
}

type verifyManifest struct {
	Outputs map[string]string `json:"outputs,omitempty"`
}

func runVerifier(
	ctx context.Context,
	config *Config,
	store *RunStore,
	attempt int,
) (VerifyResult, error) {
	attemptDir, err := store.EnsureAttempt(attempt)
	if err != nil {
		return VerifyResult{}, err
	}
	logPath, err := nextAvailablePath(attemptDir, "verify.log")
	if err != nil {
		return VerifyResult{}, err
	}
	resultPath, err := nextAvailablePath(attemptDir, "result.json")
	if err != nil {
		return VerifyResult{}, err
	}
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o600)
	if err != nil {
		return VerifyResult{}, fmt.Errorf("create verifier log: %w", err)
	}
	defer logFile.Close()

	environment := trustedEnvironment(config, config.Verify.PassEnv, attempt, attemptDir, resultPath)
	verifyContext, cancel := context.WithTimeout(ctx, time.Duration(config.Verify.Timeout))
	commandResult, runErr := runProcess(
		verifyContext,
		config.BaseDir,
		config.Verify.Command,
		environment,
		logFile,
		logFile,
	)
	contextErr := verifyContext.Err()
	cancel()
	result := VerifyResult{LogPath: logPath, ExitCode: commandResult.ExitCode}
	switch {
	case runErr != nil && contextErr == nil:
		return result, runErr
	case ctx.Err() != nil:
		return result, ctx.Err()
	case commandResult.TimedOut || errors.Is(contextErr, context.DeadlineExceeded):
		result.Outcome = VerifyInfrastructureFailure
		result.Failure = fmt.Sprintf("verifier timed out after %s", time.Duration(config.Verify.Timeout))
	case commandResult.ExitCode == 0:
		result.Outcome = VerifySuccess
		outputs, err := loadVerifierOutputs(resultPath, config.Candidate)
		if err != nil {
			return result, err
		}
		candidateOutputs, err := loadCandidateOutputs(config.Candidate)
		if err != nil {
			result.Outcome = VerifyCandidateFailure
			result.Failure = "invalid candidate output manifest: " + err.Error()
			return result, nil
		}
		for name, output := range candidateOutputs {
			if _, exists := outputs[name]; exists {
				result.Outcome = VerifyCandidateFailure
				result.Failure = fmt.Sprintf("candidate output %q conflicts with verifier output", name)
				return result, nil
			}
			outputs[name] = output
		}
		result.Outputs = outputs
	case commandResult.ExitCode == 1:
		result.Outcome = VerifyCandidateFailure
		result.Failure = "verifier rejected the candidate"
	default:
		result.Outcome = VerifyInfrastructureFailure
		result.Failure = fmt.Sprintf("verifier exited with status %d", commandResult.ExitCode)
	}
	return result, nil
}

func trustedEnvironment(config *Config, passEnv []string, attempt int, evidenceDir, resultPath string) []string {
	names := []string{"HOME", "LANG", "LC_ALL", "LOGNAME", "PATH", "SHELL", "TERM", "TMPDIR", "USER"}
	names = append(names, passEnv...)
	environment := selectedEnvironment(names)
	environment = append(environment,
		fmt.Sprintf("ALO_ATTEMPT=%d", attempt),
		"ALO_EVIDENCE_DIR="+evidenceDir,
	)
	if resultPath != "" {
		environment = append(environment, "ALO_RESULT="+resultPath)
	}
	for _, name := range sortedKeys(config.Parameters) {
		environment = append(environment,
			"ALO_PARAMETER_"+environmentName(name)+"="+config.Parameters[name],
		)
	}
	environment = append(environment, "ALO_CANDIDATE="+config.Candidate)
	for _, name := range sortedKeys(config.References) {
		environment = append(environment,
			"ALO_REFERENCE_"+environmentName(name)+"="+config.References[name],
		)
	}
	return environment
}

func selectedEnvironment(names []string) []string {
	seen := make(map[string]bool)
	var result []string
	for _, name := range names {
		if seen[name] {
			continue
		}
		seen[name] = true
		if value, exists := os.LookupEnv(name); exists {
			result = append(result, name+"="+value)
		}
	}
	sort.Strings(result)
	return result
}

func loadVerifierOutputs(path, candidate string) (map[string]OutputRecord, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return make(map[string]OutputRecord), nil
	}
	if err != nil {
		return nil, fmt.Errorf("read verifier result: %w", err)
	}
	var manifest verifyManifest
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil {
		return nil, fmt.Errorf("decode verifier result: %w", err)
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return nil, errors.New("verifier result contains trailing JSON")
	}
	result := make(map[string]OutputRecord, len(manifest.Outputs))
	for name, outputPath := range manifest.Outputs {
		if !parameterNamePattern.MatchString(name) {
			return nil, fmt.Errorf("output name %q must match %s", name, parameterNamePattern)
		}
		record, err := validateOutput(outputPath, candidate)
		if err != nil {
			return nil, fmt.Errorf("output %q: %w", name, err)
		}
		result[name] = record
	}
	return result, nil
}

func loadCandidateOutputs(candidate string) (map[string]OutputRecord, error) {
	path := filepath.Join(candidate, ".alo", "outputs.json")
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return make(map[string]OutputRecord), nil
	}
	if err != nil {
		return nil, fmt.Errorf("inspect %s: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s must be a regular file, not a symlink", path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var manifest verifyManifest
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil {
		return nil, fmt.Errorf("decode %s: %w", path, err)
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return nil, fmt.Errorf("%s contains trailing JSON", path)
	}
	result := make(map[string]OutputRecord, len(manifest.Outputs))
	for name, relative := range manifest.Outputs {
		if !parameterNamePattern.MatchString(name) {
			return nil, fmt.Errorf("output name %q must match %s", name, parameterNamePattern)
		}
		if filepath.IsAbs(relative) {
			return nil, fmt.Errorf("output %q path must be relative to the candidate", name)
		}
		record, err := validateOutput(filepath.Join(candidate, relative), candidate)
		if err != nil {
			return nil, fmt.Errorf("output %q: %w", name, err)
		}
		result[name] = record
	}
	return result, nil
}

func validateOutput(path, candidate string) (OutputRecord, error) {
	if !filepath.IsAbs(path) {
		return OutputRecord{}, errors.New("path must be absolute")
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return OutputRecord{}, fmt.Errorf("resolve path: %w", err)
	}
	resolvedRoot, err := filepath.EvalSymlinks(candidate)
	if err != nil {
		return OutputRecord{}, fmt.Errorf("resolve candidate root: %w", err)
	}
	if resolved != resolvedRoot && !strings.HasPrefix(resolved, resolvedRoot+string(filepath.Separator)) {
		return OutputRecord{}, errors.New("path is outside the candidate")
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return OutputRecord{}, err
	}
	if !info.Mode().IsRegular() {
		return OutputRecord{}, errors.New("path is not a regular file")
	}
	digest, err := hashFile(resolved)
	if err != nil {
		return OutputRecord{}, err
	}
	return OutputRecord{Path: resolved, SHA256: digest, Size: info.Size()}, nil
}

func hashFile(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func nextAvailablePath(directory, name string) (string, error) {
	candidate := filepath.Join(directory, name)
	if _, err := os.Stat(candidate); os.IsNotExist(err) {
		return candidate, nil
	} else if err != nil {
		return "", err
	}
	extension := filepath.Ext(name)
	base := strings.TrimSuffix(name, extension)
	for sequence := 2; ; sequence++ {
		candidate = filepath.Join(directory, fmt.Sprintf("%s-%02d%s", base, sequence, extension))
		if _, err := os.Stat(candidate); os.IsNotExist(err) {
			return candidate, nil
		} else if err != nil {
			return "", err
		}
	}
}

func sortedKeys[V any](values map[string]V) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
