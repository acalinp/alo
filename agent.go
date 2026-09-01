package alo

import (
	"context"
	"encoding/json"
	"io"
)

const agentBlockedPrefix = "ALO_BLOCKED:"

type AgentRequest struct {
	Goal       string            `json:"goal"`
	Attempt    int               `json:"attempt"`
	Parameters map[string]string `json:"parameters,omitempty"`
	Candidate  string            `json:"candidate"`
	References map[string]string `json:"references,omitempty"`
	Devices    []string          `json:"devices,omitempty"`
	Evidence   string            `json:"evidence"`
	Cache      string            `json:"cache"`
	Session    string            `json:"session"`
}

type AgentTurn struct {
	Config      *Config
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
	Config  *Config
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

func writeAgentRequest(path string, request AgentRequest) error {
	data, err := encodeAgentRequest(request)
	if err != nil {
		return err
	}
	return atomicWrite(path, data, 0o600)
}

func writeWorkshopRequest(path string, request AgentRequest) error {
	data, err := encodeAgentRequest(request)
	if err != nil {
		return err
	}
	return replaceUntrustedFile(path, data)
}

func encodeAgentRequest(request AgentRequest) ([]byte, error) {
	data, err := json.MarshalIndent(request, "", "  ")
	if err != nil {
		return nil, err
	}
	data = append(data, '\n')
	return data, nil
}
