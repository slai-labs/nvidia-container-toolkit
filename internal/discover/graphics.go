/**
# Copyright (c) 2022, NVIDIA CORPORATION.  All rights reserved.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.
**/

package discover

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/NVIDIA/nvidia-container-toolkit/internal/config/image"
	"github.com/NVIDIA/nvidia-container-toolkit/internal/info/drm"
	"github.com/NVIDIA/nvidia-container-toolkit/internal/info/proc"
	"github.com/NVIDIA/nvidia-container-toolkit/internal/lookup"
	"github.com/container-orchestrated-devices/container-device-interface/pkg/cdi"
	"github.com/sirupsen/logrus"
)

// NewGraphicsDiscoverer returns the discoverer for graphics tools such as Vulkan.
func NewGraphicsDiscoverer(logger *logrus.Logger, devices image.VisibleDevices, cfg *Config) (Discover, error) {
	root := cfg.Root

	locator, err := lookup.NewLibraryLocator(logger, root)
	if err != nil {
		return nil, fmt.Errorf("failed to construct library locator: %v", err)
	}
	libraries := NewMounts(
		logger,
		locator,
		root,
		[]string{
			"libnvidia-egl-gbm.so",
		},
	)

	jsonMounts := NewMounts(
		logger,
		lookup.NewFileLocator(logger, root),
		root,
		[]string{
			// TODO: We should handle this more cleanly
			"/etc/glvnd/egl_vendor.d/10_nvidia.json",
			"/etc/vulkan/icd.d/nvidia_icd.json",
			"/etc/vulkan/implicit_layer.d/nvidia_layers.json",
			"/usr/share/glvnd/egl_vendor.d/10_nvidia.json",
			"/usr/share/vulkan/icd.d/nvidia_icd.json",
			"/usr/share/vulkan/implicit_layer.d/nvidia_layers.json",
			"/usr/share/egl/egl_external_platform.d/15_nvidia_gbm.json",
		},
	)

	drmDeviceNodes, err := newDRMDeviceDiscoverer(logger, devices, root)
	if err != nil {
		return nil, fmt.Errorf("failed to create DRM device discoverer: %v", err)
	}

	drmByPathSymlinks := newCreateDRMByPathSymlinks(logger, drmDeviceNodes, cfg)

	discover := Merge(
		Merge(drmDeviceNodes, drmByPathSymlinks),
		libraries,
		jsonMounts,
	)

	return discover, nil
}

type drmDevicesByPath struct {
	None
	logger                  *logrus.Logger
	lookup                  lookup.Locator
	nvidiaCTKExecutablePath string
	root                    string
	devicesFrom             Discover
}

// newCreateDRMByPathSymlinks creates a discoverer for a hook to create the by-path symlinks for DRM devices discovered by the specified devices discoverer
func newCreateDRMByPathSymlinks(logger *logrus.Logger, devices Discover, cfg *Config) Discover {
	d := drmDevicesByPath{
		logger:                  logger,
		lookup:                  lookup.NewExecutableLocator(logger, cfg.Root),
		nvidiaCTKExecutablePath: cfg.NVIDIAContainerToolkitCLIExecutablePath,
		root:                    cfg.Root,
		devicesFrom:             devices,
	}

	return &d
}

// Hooks returns a hook to create the symlinks from the required CSV files
func (d drmDevicesByPath) Hooks() ([]Hook, error) {
	devices, err := d.devicesFrom.Devices()
	if err != nil {
		return nil, fmt.Errorf("failed to discover devices for by-path symlinks: %v", err)
	}
	if len(devices) == 0 {
		return nil, nil
	}

	hookPath := nvidiaCTKDefaultFilePath
	targets, err := d.lookup.Locate(d.nvidiaCTKExecutablePath)
	if err != nil {
		d.logger.Warnf("Failed to locate %v: %v", d.nvidiaCTKExecutablePath, err)
	} else if len(targets) == 0 {
		d.logger.Warnf("%v not found", d.nvidiaCTKExecutablePath)
	} else {
		d.logger.Debugf("Found %v candidates: %v", d.nvidiaCTKExecutablePath, targets)
		hookPath = targets[0]
	}
	d.logger.Debugf("Using NVIDIA Container Toolkit CLI path %v", hookPath)

	args := []string{hookPath, "hook", "create-symlinks"}
	links, err := d.getSpecificLinkArgs(devices)
	if err != nil {
		return nil, fmt.Errorf("failed to determine specific links: %v", err)
	}
	for _, l := range links {
		args = append(args, "--link", l)
	}

	h := Hook{
		Lifecycle: cdi.CreateContainerHook,
		Path:      hookPath,
		Args:      args,
	}

	return []Hook{h}, nil
}

// getSpecificLinkArgs returns the required specic links that need to be created
func (d drmDevicesByPath) getSpecificLinkArgs(devices []Device) ([]string, error) {
	selectedDevices := make(map[string]bool)
	for _, d := range devices {
		selectedDevices[filepath.Base(d.HostPath)] = true
	}

	linkLocator := lookup.NewFileLocator(d.logger, d.root)
	candidates, err := linkLocator.Locate("/dev/dri/by-path/pci-*-*")
	if err != nil {
		return nil, fmt.Errorf("failed to locate devices by path: %v", err)
	}

	var links []string
	for _, c := range candidates {
		device, err := os.Readlink(c)
		if err != nil {
			d.logger.Warningf("Failed to evaluate symlink %v; ignoring", c)
			continue
		}

		if selectedDevices[filepath.Base(device)] {
			d.logger.Debugf("adding device symlink %v -> %v", c, device)
			links = append(links, fmt.Sprintf("%v::%v", device, c))
		}
	}

	return links, nil
}

// newDRMDeviceDiscoverer creates a discoverer for the DRM devices associated with the requested devices.
func newDRMDeviceDiscoverer(logger *logrus.Logger, devices image.VisibleDevices, root string) (Discover, error) {
	allDevices := NewDeviceDiscoverer(
		logger,
		lookup.NewCharDeviceLocator(logger, root),
		root,
		[]string{
			"/dev/dri/card*",
			"/dev/dri/renderD*",
		},
	)

	filter, err := newDRMDeviceFilter(logger, devices, root)
	if err != nil {
		return nil, fmt.Errorf("failed to construct DRM device filter: %v", err)
	}

	// We return a discoverer that applies the DRM device filter created above to all discovered DRM device nodes.
	d := newFilteredDisoverer(
		logger,
		allDevices,
		filter,
	)

	return d, err
}

// newDRMDeviceFilter creates a filter that matches DRM devices nodes for the visible devices.
func newDRMDeviceFilter(logger *logrus.Logger, devices image.VisibleDevices, root string) (Filter, error) {
	gpuInformationPaths, err := proc.GetInformationFilePaths(root)
	if err != nil {
		return nil, fmt.Errorf("failed to read GPU information: %v", err)
	}

	var selectedBusIds []string
	for _, f := range gpuInformationPaths {
		info, err := proc.ParseGPUInformationFile(f)
		if err != nil {
			return nil, fmt.Errorf("failed to parse %v: %v", f, err)
		}
		uuid := info[proc.GPUInfoGPUUUID]
		busID := info[proc.GPUInfoBusLocation]
		minor := info[proc.GPUInfoDeviceMinor]

		if devices.Has(minor) || devices.Has(uuid) || devices.Has(busID) {
			selectedBusIds = append(selectedBusIds, busID)
		}
	}

	filter := make(selectDeviceByPath)
	for _, busID := range selectedBusIds {
		drmDeviceNodes, err := drm.GetDeviceNodesByBusID(busID)
		if err != nil {
			return nil, fmt.Errorf("failed to determine DRM devices for %v: %v", busID, err)
		}
		for _, drmDeviceNode := range drmDeviceNodes {
			filter[filepath.Join(drmDeviceNode)] = true
		}
	}

	return filter, nil
}

// selectDeviceByPath is a filter that allows devices to be selected by the path
type selectDeviceByPath map[string]bool

var _ Filter = (*selectDeviceByPath)(nil)

// DeviceIsSelected determines whether the device's path has been selected
func (s selectDeviceByPath) DeviceIsSelected(device Device) bool {
	return s[device.Path]
}

// MountIsSelected is always true
func (s selectDeviceByPath) MountIsSelected(Mount) bool {
	return true
}

// HookIsSelected is always true
func (s selectDeviceByPath) HookIsSelected(Hook) bool {
	return true
}
