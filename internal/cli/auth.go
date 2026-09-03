package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"alo/internal/auth"

	"golang.org/x/sys/unix"
	"golang.org/x/term"
)

var credentialStore auth.Store = auth.KeyringStore{}

func authCommand(ctx context.Context, args []string, input io.Reader, output, errorOutput io.Writer) int {
	if len(args) != 2 || args[1] != "openrouter" {
		fmt.Fprintln(errorOutput, "usage: alo auth set|status|delete openrouter")
		return 2
	}
	switch args[0] {
	case "set":
		secret, err := readSecret(ctx, input, output, "OpenRouter API key: ")
		if err != nil {
			if errors.Is(err, context.Canceled) {
				fmt.Fprintln(output)
				return 130
			}
			fmt.Fprintln(errorOutput, err)
			return 2
		}
		if secret == "" {
			fmt.Fprintln(errorOutput, "OpenRouter API key may not be empty")
			return 2
		}
		if err := credentialStore.Set(args[1], secret); err != nil {
			fmt.Fprintln(errorOutput, err)
			return 2
		}
		fmt.Fprintln(output, "Stored OpenRouter credential in the OS keyring.")
		return 0
	case "status":
		_, err := credentialStore.Get(args[1])
		if errors.Is(err, auth.ErrNotFound) {
			fmt.Fprintln(output, "OpenRouter credential is not configured.")
			return 1
		}
		if err != nil {
			fmt.Fprintln(errorOutput, err)
			return 2
		}
		fmt.Fprintln(output, "OpenRouter credential is configured.")
		return 0
	case "delete":
		err := credentialStore.Delete(args[1])
		if errors.Is(err, auth.ErrNotFound) {
			fmt.Fprintln(output, "OpenRouter credential is not configured.")
			return 0
		}
		if err != nil {
			fmt.Fprintln(errorOutput, err)
			return 2
		}
		fmt.Fprintln(output, "Deleted OpenRouter credential from the OS keyring.")
		return 0
	default:
		fmt.Fprintln(errorOutput, "usage: alo auth set|status|delete openrouter")
		return 2
	}
}

func readSecret(ctx context.Context, input io.Reader, output io.Writer, prompt string) (string, error) {
	type readResult struct {
		value string
		err   error
	}
	result := make(chan readResult, 1)
	if file, ok := input.(*os.File); ok && term.IsTerminal(int(file.Fd())) {
		fmt.Fprint(output, prompt)
		fd := int(file.Fd())
		state, err := unix.IoctlGetTermios(fd, unix.TCGETS)
		if err != nil {
			return "", err
		}
		hidden := *state
		hidden.Lflag &^= unix.ECHO
		if err := unix.IoctlSetTermios(fd, unix.TCSETS, &hidden); err != nil {
			return "", err
		}
		defer unix.IoctlSetTermios(fd, unix.TCSETS, state)
		go func() {
			value, err := bufio.NewReader(input).ReadString('\n')
			result <- readResult{value: value, err: err}
		}()
	} else {
		go func() {
			value, err := bufio.NewReader(input).ReadString('\n')
			result <- readResult{value: value, err: err}
		}()
	}
	var read readResult
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case read = <-result:
	}
	if file, ok := input.(*os.File); ok && term.IsTerminal(int(file.Fd())) {
		fmt.Fprintln(output)
	}
	value, err := read.value, read.err
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	return strings.TrimSpace(value), nil
}
