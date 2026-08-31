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

	environment := verifierEnvironment(config, attempt, attemptDir, resultPath)
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
		outputs, err := loadVerifierOutputs(resultPath, config.Candidates)
		if err != nil {
			return result, err
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

func verifierEnvironment(config *Config, attempt int, evidenceDir, resultPath string) []string {
	names := []string{"HOME", "LANG", "LC_ALL", "LOGNAME", "PATH", "SHELL", "TERM", "TMPDIR", "USER"}
	names = append(names, config.Verify.PassEnv...)
	environment := selectedEnvironment(names)
	environment = append(environment,
		fmt.Sprintf("ALO_ATTEMPT=%d", attempt),
		"ALO_EVIDENCE_DIR="+evidenceDir,
		"ALO_RESULT="+resultPath,
	)
	for _, name := range sortedKeys(config.Parameters) {
		environment = append(environment,
			"ALO_PARAMETER_"+environmentName(name)+"="+config.Parameters[name],
		)
	}
	for _, name := range sortedKeys(config.Candidates) {
		environment = append(environment,
			"ALO_CANDIDATE_"+environmentName(name)+"="+config.Candidates[name],
		)
	}
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

func loadVerifierOutputs(path string, candidates map[string]string) (map[string]OutputRecord, error) {
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
		record, err := validateOutput(outputPath, candidates)
		if err != nil {
			return nil, fmt.Errorf("output %q: %w", name, err)
		}
		result[name] = record
	}
	return result, nil
}

func validateOutput(path string, candidates map[string]string) (OutputRecord, error) {
	if !filepath.IsAbs(path) {
		return OutputRecord{}, errors.New("path must be absolute")
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return OutputRecord{}, fmt.Errorf("resolve path: %w", err)
	}
	contained := false
	for _, root := range candidates {
		resolvedRoot, err := filepath.EvalSymlinks(root)
		if err != nil {
			return OutputRecord{}, fmt.Errorf("resolve candidate root: %w", err)
		}
		if resolved == resolvedRoot || strings.HasPrefix(resolved, resolvedRoot+string(filepath.Separator)) {
			contained = true
			break
		}
	}
	if !contained {
		return OutputRecord{}, errors.New("path is outside every candidate")
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
