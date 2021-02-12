// +build linux

package kernel

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/DataDog/datadog-agent/pkg/util/log"

	"github.com/DataDog/nikos/apt"
	"github.com/DataDog/nikos/cos"
	"github.com/DataDog/nikos/rpm"
	"github.com/DataDog/nikos/types"
	"github.com/DataDog/nikos/wsl"
)

// customLogger is a wrapper around our logging utility which allows nikos to use our logging functions
type customLogger struct{}

func (c customLogger) Debug(args ...interface{})                 { log.Debug(args) }
func (c customLogger) Info(args ...interface{})                  { log.Info(args) }
func (c customLogger) Debugf(format string, args ...interface{}) { log.Debugf(format, args...) }
func (c customLogger) Infof(format string, args ...interface{})  { log.Infof(format, args...) }
func (c customLogger) Warnf(format string, args ...interface{})  { log.Warnf(format, args...) }
func (c customLogger) Errorf(format string, args ...interface{}) { log.Errorf(format, args...) }

var _ types.Logger = customLogger{}

func DownloadHeaders(outputDirPath string) ([]string, error) {
	var (
		target    types.Target
		backend   types.Backend
		outputDir string
		err       error
	)

	if outputDir, err = createOutputDir(outputDirPath); err != nil {
		return nil, fmt.Errorf("unable create output directory %s: %s", outputDirPath, err)
	}

	if target, err = getHeaderDownloadTarget(); err != nil {
		return nil, fmt.Errorf("failed to retrieve target information: %s", err)
	}

	log.Infof("Downloading kernel headers for target distribution %s, release %s, kernel %s",
		target.Distro.Display,
		target.Distro.Release,
		target.Uname.Kernel,
	)
	log.Debugf("Target OSRelease: %s", target.OSRelease)

	if backend, err = getHeaderDownloadBackend(&target); err != nil {
		return nil, fmt.Errorf("unable to get kernel header download backend: %s", err)
	}

	if err = backend.GetKernelHeaders(outputDir); err != nil {
		return nil, fmt.Errorf("failed to download kernel headers: %s", err)
	}

	headerDirs := []string{fmt.Sprintf(outputDir+kernelModulesPath, target.Uname.Kernel)}
	if target.Distro.Display == "Debian" {
		headerDirs = append(headerDirs, fmt.Sprintf(outputDir+"/lib/modules/%s/source", target.Uname.Kernel))
	}
	return headerDirs, nil
}

func getHeaderDownloadTarget() (types.Target, error) {
	target, err := types.NewTarget()
	if err != nil {
		return types.Target{}, err
	}

	if _, err := os.Stat("/run/WSL"); err == nil {
		target.Distro.Display = "wsl"
	} else if id := target.OSRelease["ID"]; target.Distro.Display == "" && id != "" {
		target.Distro.Display = id
	}

	return target, nil
}

func getHeaderDownloadBackend(target *types.Target) (backend types.Backend, err error) {
	logger := customLogger{}
	switch target.Distro.Display {
	case "Fedora", "RHEL":
		backend, err = rpm.NewRedHatBackend(target, "/etc/yum.repos.d", logger)
	case "CentOS":
		backend, err = rpm.NewCentOSBackend(target, "/etc/yum.repos.d", logger)
	case "openSUSE":
		backend, err = rpm.NewOpenSUSEBackend(target, "/etc/zypp/repos.d", logger)
	case "SLE":
		backend, err = rpm.NewSLESBackend(target, "/etc/zypp/repos.d", logger)
	case "Debian", "Ubuntu":
		backend, err = apt.NewBackend(target, "/etc/apt", logger)
	case "cos":
		backend, err = cos.NewBackend(target, logger)
	case "wsl":
		backend, err = wsl.NewBackend(target, logger)
	default:
		err = fmt.Errorf("Unsupported distribution '%s'", target.Distro.Display)
	}
	return
}

func createOutputDir(path string) (string, error) {
	absolutePath, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("unable to get absolute path: %s", err)
	}

	err = os.MkdirAll(absolutePath, 0755)
	return absolutePath, err
}
