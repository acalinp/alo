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
	Candidates map[string]string `json:"candidates"`
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

type AgentRunner interface {
	ResolveImage(context.Context) (string, error)
	Turn(context.Context, AgentTurn) (AgentResult, error)
}

func writeAgentRequest(path string, request AgentRequest) error {
	data, err := json.MarshalIndent(request, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return atomicWrite(path, data, 0o600)
}
