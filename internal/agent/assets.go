package agent

import _ "embed"

//go:embed Containerfile
var containerfile []byte

//go:embed run-agent
var runner []byte

//go:embed instructions.md
var instructions []byte

func Containerfile() []byte { return containerfile }

func Runner() []byte { return runner }

func Instructions() []byte { return instructions }
