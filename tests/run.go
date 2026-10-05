// Run from the backend root: go run ./tests/run.go [test|vet] [Go flags].
package main

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		if exit, ok := err.(*exec.ExitError); ok {
			os.Exit(exit.ExitCode())
		}
		os.Exit(1)
	}
}

func run() error {
	root, err := os.Getwd()
	if err != nil {
		return err
	}
	module, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil || !strings.HasPrefix(string(module), "module hoanxu\n") && !strings.HasPrefix(string(module), "module hoanxu\r\n") {
		return fmt.Errorf("run this command from the backend repository root")
	}
	replace := make(map[string]string)
	for _, group := range []string{"unit", "integration"} {
		base := filepath.Join(root, "tests", group)
		err := filepath.WalkDir(base, func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), "_test.go") {
				return nil
			}
			rel, err := filepath.Rel(base, path)
			if err != nil {
				return err
			}
			parts := strings.Split(filepath.ToSlash(rel), "/")
			if len(parts) != 2 {
				return fmt.Errorf("expected tests/%s/PACKAGE/FILE_test.go: %s", group, rel)
			}
			virtual := filepath.Join(root, "internal", parts[0], parts[1])
			if _, err := os.Stat(filepath.Dir(virtual)); err != nil {
				return err
			}
			if _, exists := replace[virtual]; exists {
				return fmt.Errorf("duplicate test destination: %s", virtual)
			}
			if _, err := os.Stat(virtual); err == nil {
				return fmt.Errorf("test still exists outside tests/: %s", virtual)
			} else if !os.IsNotExist(err) {
				return err
			}
			replace[virtual] = path
			return nil
		})
		if err != nil {
			return err
		}
	}
	if len(replace) == 0 {
		return fmt.Errorf("no test files found")
	}
	data, err := json.Marshal(struct{ Replace map[string]string }{replace})
	if err != nil {
		return err
	}
	results := filepath.Join(root, "tests", "results")
	if err := os.MkdirAll(results, 0700); err != nil {
		return err
	}
	file, err := os.CreateTemp(results, "overlay-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	_, writeErr := file.Write(data)
	closeErr := file.Close()
	if writeErr != nil {
		return writeErr
	}
	if closeErr != nil {
		return closeErr
	}
	mode, args := "test", os.Args[1:]
	if len(args) > 0 && (args[0] == "test" || args[0] == "vet") {
		mode, args = args[0], args[1:]
	}
	flags := []string{mode, "-overlay", file.Name()}
	if mode == "test" {
		flags = append(flags, "-count=1")
	}
	flags = append(flags, args...)
	flags = append(flags, "./...")
	goName := "go"
	if runtime.GOOS == "windows" {
		goName += ".exe"
	}
	fmt.Fprintf(os.Stderr, "Loaded %d test files from tests/ (%s)\n", len(replace), mode)
	command := exec.Command(filepath.Join(runtime.GOROOT(), "bin", goName), flags...)
	command.Dir = root
	command.Stdin, command.Stdout, command.Stderr = os.Stdin, os.Stdout, os.Stderr
	return command.Run()
}
