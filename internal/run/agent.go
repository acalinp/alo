package run

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"

	"alo/internal/config"
)

const AgentBlockedPrefix = "ALO_BLOCKED:"

type AgentRequest struct {
	Goal       string            `json:"goal"`
	Attempt    int               `json:"attempt"`
	Provider   string            `json:"provider"`
	Model      string            `json:"model"`
	Thinking   string            `json:"thinking"`
	ZDR        bool              `json:"zdr,omitempty"`
	Parameters map[string]string `json:"parameters,omitempty"`
	Candidate  string            `json:"candidate"`
	References map[string]string `json:"references,omitempty"`
	Devices    []string          `json:"devices,omitempty"`
	Evidence   string            `json:"evidence"`
	Cache      string            `json:"cache"`
	Session    string            `json:"session"`
}

type AgentTurn struct {
	Config      *config.Config
	Store       *RunStore
	Attempt     int
	ImageID     string
	LogPath     string
	RequestPath string
	Console     io.Writer
}

type AgentResult struct {
	BlockedReason string
}

type ExerciseTurn struct {
	Config  *config.Config
	Store   *RunStore
	Attempt int
	ImageID string
	LogPath string
	Console io.Writer
}

type ExerciseResult struct {
	ExitCode int
}

type Runtime interface {
	ResolveImage(context.Context) (string, error)
	Exercise(context.Context, ExerciseTurn) (ExerciseResult, error)
	Turn(context.Context, AgentTurn) (AgentResult, error)
	RemoveWorkshop(context.Context, *RunStore) error
}

func WriteAgentRequest(path string, request AgentRequest) error {
	data, err := encodeAgentRequest(request)
	if err != nil {
		return err
	}
	return atomicWrite(path, data, 0o600)
}

func WriteWorkshopRequest(path string, request AgentRequest) error {
	data, err := encodeAgentRequest(request)
	if err != nil {
		return err
	}
	return replaceUntrustedFile(path, data)
}

func replaceUntrustedFile(path string, data []byte) error {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, writeErr := file.Write(data)
	closeErr := file.Close()
	return errors.Join(writeErr, closeErr)
}

func encodeAgentRequest(request AgentRequest) ([]byte, error) {
	data, err := json.MarshalIndent(request, "", "  ")
	if err != nil {
		return nil, err
	}
	data = append(data, '\n')
	return data, nil
}
