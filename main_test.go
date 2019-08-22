package main

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/VirtusLab/render/constants"

	"github.com/VirtusLab/go-extended/pkg/files"
	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
)

const (
	testBinaryName = "testrender"
	killIn         = 10 * time.Second
)

var (
	exeSuffix string // ".exe" on Windows
)

func init() {
	switch runtime.GOOS {
	case "windows":
		exeSuffix = ".exe"
	}
}

// The TestMain function creates a the binary for testing purposes
// and deletes it after the tests have been run.
func TestMain(m *testing.M) {
	// build the test binary
	args := []string{"build", "-o", testBinaryName + exeSuffix}
	out, err := exec.Command("go", args...).CombinedOutput()
	if err != nil {
		_, err := fmt.Fprintf(os.Stderr, "building %s failed: %v\n%s", testBinaryName, err, out)
		if err != nil {
			logrus.Errorf("Unexpected: %s", err)
		}
		os.Exit(2)
	}
	// remove test binary
	defer func() { _ = os.Remove(testBinaryName + exeSuffix) }()

	flag.Parse()
	merr := m.Run()
	if merr != 0 {
		fmt.Printf("Main tests failed.\n")
		os.Exit(merr)
	}

	os.Exit(0)
}

func run(args ...string) (stdout, stderr string, err error) {
	return runStdin(nil, args...)
}

func runStdin(stdin *string, args ...string) (stdout, stderr string, err error) {
	prog := "./" + testBinaryName + exeSuffix
	// always add debug flag
	newargs := append([]string{"-d"}, args...)
	ctx, cancel := context.WithTimeout(context.TODO(), killIn)
	defer cancel()

	fmt.Printf("$ %s %s\n\n", prog, strings.Join(newargs, " "))
	stdout, stderr, err = sh(ctx, stdin, prog, newargs...)
	fmt.Printf("stdout:\n%s\n\n", stdout)
	fmt.Printf("stderr:\n%s\n\n", stderr)

	return
}

func sh(ctx context.Context, stdin *string, prog string, args ...string) (stdout, stderr string, err error) {
	cmd := exec.CommandContext(ctx, prog, args...)

	var stdinPipe io.WriteCloser
	if stdin != nil {
		stdinPipe, err = cmd.StdinPipe()
		if err != nil {
			err = errors.Wrap(err, "can't open stdin pipe")
			return
		}
		defer func() { _ = stdinPipe.Close() }() // just to be sure
	}

	// Set output to Byte Buffers
	var outb, errb bytes.Buffer
	cmd.Stdout = &outb
	cmd.Stderr = &errb

	if err = cmd.Start(); err != nil {
		return outb.String(), errb.String(), err
	}

	if stdin != nil {
		if _, err = io.WriteString(stdinPipe, *stdin); err != nil {
			err = errors.Wrap(err, "error writing to stdin pipe")
			return
		}
		err = stdinPipe.Close() // must be called to flush the buffers
		if err != nil {
			err = errors.Wrap(err, "error closing the stdin pipe")
			return
		}
	}

	err = cmd.Wait()
	stdout = outb.String()
	stderr = errb.String()

	return
}

func TestHelp(t *testing.T) {
	stdout, _, err := run("-h")
	if err != nil {
		t.Fatalf("output: '%s', error: %v", string(stdout), err)
	}

	expected := fmt.Sprintf("%s - %s", constants.Name, constants.Description)
	if !strings.Contains(stdout, expected) {
		t.Fatalf("expected contains:\n%s\ngot:\n%s", expected, stdout)
	}
}

func TestRender(t *testing.T) {
	stdout, _, err := run("--config", "examples/example.config.yaml", "--in", "examples/example.yaml.tmpl")
	if err != nil {
		t.Fatalf("output: '%s', error: %v", string(stdout), err)
	}

	expectedPath := "examples/example.yaml.expected"
	expected, err := files.ReadInput(expectedPath)

	assert.NoErrorf(t, err, "cannot read test file: '%s'", expectedPath)
	assert.Equal(t, string(expected), stdout)
}

func TestDirRender(t *testing.T) {
	stdout, _, err := run("--config", "examples/example.config.yaml", "--indir", "examples/directory-test")
	assert.NoError(t, err)

	expectedPath := "examples/directory-test/example.yaml"
	expected, err := files.ReadInput(expectedPath)
	assert.NoErrorf(t, err, "cannot read test file: '%s'", expectedPath)

	expectedSubPath := "examples/directory-test/subdirectory/example.yaml"
	expectedSub, err := files.ReadInput(expectedSubPath)
	assert.NoErrorf(t, err, "cannot read test file: '%s'", expectedSubPath)

	assert.Contains(t, string(expected), stdout, "name: render")
	assert.Contains(t, string(expectedSub), stdout, "name: render")
}

func TestNoArgs(t *testing.T) {
	stdout, stderr, err := run()
	assert.EqualError(t, err, "exit status 1")

	expectedStdout := ``
	assert.Equal(t, expectedStdout, stdout)

	expectedStderr := `expected either stdin, --indir or --in parameter, for usage use --help`
	assert.Contains(t, stderr, expectedStderr)
}

func TestEmpty(t *testing.T) {
	stdin := ""
	stdout, _, err := runStdin(&stdin)

	assert.NoError(t, err)
	assert.Equal(t, "", stdout)
}

func TestSimple(t *testing.T) {
	stdin := "test-{{ .something }}-test"
	stdout, _, err := runStdin(&stdin, "--var=something=test")

	assert.NoError(t, err)
	assert.Equal(t, "test-test-test", stdout)
}

func TestMissingKeyError(t *testing.T) {
	stdin := "{{ .missing }}"
	stdout, stderr, err := runStdin(&stdin)

	assert.EqualError(t, err, "exit status 1")
	assert.Equal(t, "", stdout)
	assert.Contains(t, stderr, "stdin:1:3")
	assert.Contains(t, stderr, "map has no entry for key \\\"missing\\\"")
}

func TestMissingKeyInvalid(t *testing.T) {
	stdin := "{{ .missing }}"
	stdout, stderr, err := runStdin(&stdin, "--unsafe-ignore-missing-keys")

	assert.NoError(t, err)
	assert.Equal(t, "<no value>", stdout)
	assert.NotContains(t, stderr, "error")
}

func TestVars(t *testing.T) {
	stdin := `{{ .first }} {{ .second }} {{ .third.nested }}`
	stdout, _, err := runStdin(&stdin,
		"--var", "first=value1",
		"--var", `second="value 2"`,
		"--var", "third.nested='and value 3'",
	)

	assert.NoError(t, err)
	assert.Equal(t, "value1 value 2 and value 3", stdout)
}

func TestNestedRenderOverride(t *testing.T) {
	stdin := "key: {{ .inner | render .override }}"
	stdout, _, err := runStdin(&stdin,
		"--config", "examples/nested-render.config.yaml")

	assert.NoError(t, err)
	assert.Equal(t, "key: other", stdout)
}

func TestEmptyConfigFlag(t *testing.T) {
	stdin := ""
	_, _, err := runStdin(&stdin,
		"--config", "--config", "examples/example.config.yaml")

	assert.Error(t, err)
}

func TestEmptyVarFlag(t *testing.T) {
	stdin := ""
	_, _, err := runStdin(&stdin,
		"--var", "--var", "key=value")

	assert.Error(t, err)
}

func TestRegression_11(t *testing.T) {
	t.Run("regression #11", func(t *testing.T) {
		stdin := `apiVersion: v1
kind: ResourceQuota
metadata:
  name: default
spec:
  hard:
    {{- if .resourceQuota.hard.cpu }}
    cpu: {{ .resourceQuota.hard.cpu }}
    {{- else }}
    cpu: "1"
    {{- end}}`

		expected1 := `apiVersion: v1
kind: ResourceQuota
metadata:
  name: default
spec:
  hard:
    cpu: 10`

		stdout1, stderr1, err := runStdin(&stdin,
			"--var", "resourceQuota.hard.cpu=10", "--unsafe-ignore-missing-keys")

		assert.NoError(t, err)
		assert.Equal(t, expected1, stdout1)
		assert.NotContains(t, stderr1, "error")

		expected2 := `apiVersion: v1
kind: ResourceQuota
metadata:
  name: default
spec:
  hard:
    cpu: "1"`

		stdout2, stderr2, err := runStdin(&stdin, "--unsafe-ignore-missing-keys")

		assert.NoError(t, err)
		assert.Equal(t, expected2, stdout2)
		assert.NotContains(t, stderr2, "error")
	})
}
