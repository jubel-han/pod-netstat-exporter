package cri

import (
	"fmt"
	"io/ioutil"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/sirupsen/logrus"
)

// ContainerToPID finds the PID of the given container
func ContainerToPID(hostMountPath, container string) (int, error) {
	raw := strings.Replace(container, "containerd://", "", 1)
	return getPidForContainer(hostMountPath, raw)
}

// As for now, we hard coded the container run state dir arbitrary
func findContainerdRunState() (string, error) {
	runStateDir := "/var/run/containerd/"
	return runStateDir, nil
}

// Returns the first pid in a container.
func getPidForContainer(hostMountPath, id string) (int, error) {
	pid := 0

	ctrRunRoot, err := findContainerdRunState()
	if err != nil {
		return pid, err
	}

	attempts := []string{
		filepath.Join(hostMountPath, ctrRunRoot, "io.containerd.runtime.v2.task", "k8s.io", id, "init.pid"),
		filepath.Join(hostMountPath, ctrRunRoot, "io.containerd.runtime.v1.linux", "k8s.io", id, "init.pid"),
	}

	var filename string
	for _, attempt := range attempts {
		logrus.Tracef("looking for the pid with attempt %v", attempt)

		filenames, _ := filepath.Glob(attempt)
		if len(filenames) > 1 {
			return pid, fmt.Errorf("ambiguous id supplied: %v", filenames)
		} else if len(filenames) == 1 {
			filename = filenames[0]
			logrus.Tracef("found the file name: %v", filename)

			break
		}
	}

	logrus.Tracef("Looking for container %v pid in %v", id, filename)

	if filename == "" {
		return pid, fmt.Errorf("Unable to find container: %v", id[:len(id)-1])
	}

	output, err := ioutil.ReadFile(filename)
	if err != nil {
		return pid, err
	}

	result := strings.Split(string(output), "\n")
	if len(result) == 0 || len(result[0]) == 0 {
		return pid, fmt.Errorf("No pid found for container")
	}

	pid, err = strconv.Atoi(result[0])
	if err != nil {
		return pid, fmt.Errorf("Invalid pid '%s': %s", result[0], err)
	}

	return pid, nil
}
