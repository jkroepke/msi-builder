package config

import (
	"fmt"
	"strings"
)

// maxRestartValue caps a restartResources value: one line of the
// BuilderRestartResources property, formatted by the custom action.
const maxRestartValue = 1024

// restartFile, restartProgram and restartService are restartResources keys.
const (
	restartFile    = "file"
	restartProgram = "program"
	restartService = "service"
)

// componentRestartResources checks the files, programs and services a
// component registers with the Restart Manager session (spec 17.11).
func (v *validator) componentRestartResources(cfg *Config, compPath string, comp *Component, perUser bool) {
	seen := make(map[string]bool)

	for j, res := range comp.RestartResources {
		resPath := fmt.Sprintf("%s.restartResources[%d]", compPath, j)

		key, value, ok := res.kind()
		if !ok {
			v.errorf(resPath, "set exactly one of file, program and service")

			continue
		}

		field := resPath + "." + key
		v.restartValue(field, key, value, perUser)

		dup := key + "\x00" + strings.ToLower(value)
		if seen[dup] {
			v.errorf(field, "this component already registers %s", value)
		}

		seen[dup] = true
	}

	if len(comp.RestartResources) == 0 {
		return
	}

	prop := cfg.Properties["MSIRESTARTMANAGERCONTROL"]

	switch {
	case prop == nil:
	case strings.EqualFold(prop.Value, "Disable"):
		v.warnf(compPath+".restartResources", "MSIRESTARTMANAGERCONTROL is Disable, so the installer is expected to start no "+
			"Restart Manager session and nothing is registered")
	case strings.EqualFold(prop.Value, "DisableShutdown"):
		v.warnf(compPath+".restartResources", "MSIRESTARTMANAGERCONTROL is DisableShutdown, so Restart Manager shuts "+
			"nothing down")
	}
}

// kind returns the key and the value of the one kind an entry sets, and
// false when it sets none or more than one.
func (r *RestartResource) kind() (string, string, bool) {
	key, value, count := "", "", 0

	for _, kv := range [][2]string{{restartFile, r.File}, {restartProgram, r.Program}, {restartService, r.Service}} {
		if kv[1] != "" {
			key, value = kv[0], kv[1]
			count++
		}
	}

	return key, value, count == 1
}

// restartValue checks the value of one restartResources entry.
func (v *validator) restartValue(field, key, value string, perUser bool) {
	switch {
	case strings.ContainsAny(value, "\r\n\x1f"):
		v.errorf(field, "must not contain a line break or U+001F")
	case len(value) > maxRestartValue:
		v.errorf(field, "is %d bytes, at most %d", len(value), maxRestartValue)
	case key == restartService && len(value) > maxServiceName:
		v.errorf(field, "is %d bytes, a service name holds %d", len(value), maxServiceName)
	case key == restartService && perUser:
		v.errorf(field, "%s cannot shut down services, which needs elevation", v.perUserPackage())
	case key == restartFile && (strings.HasSuffix(value, `\`) || strings.HasSuffix(value, "/")):
		v.warnf(field, "ends in a path separator; Restart Manager registers files, not folders, so list the files "+
			"that programs hold open")
	}
}
