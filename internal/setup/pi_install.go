package setup

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
)

func isPiManagedPackage(pkg string) bool {
	switch pkg {
	case piGentleEngramPackage, piLegacyGentleEngramPackage, piPreviousGentleEngramPackage, piPriorGentleEngramPackage, piPredecessorGentleEngramPackage, piFormerGentleEngramPackage, piEarlierGentleEngramPackage, piPriorReleaseGentleEngramPackage, piLastReleaseGentleEngramPackage, "npm:gentle-engram@0.4.0", "npm:gentle-engram@0.4.1", "npm:gentle-engram@0.4.2", "npm:gentle-engram@0.5.0", "npm:gentle-engram@0.5.1", "npm:gentle-engram@0.6.0", "npm:gentle-engram@0.7.0", "npm:gentle-engram@0.7.1":
		return true
	}
	return false
}

func samePiJSON(a, b json.RawMessage) bool {
	var left, right any
	leftDecoder, rightDecoder := json.NewDecoder(bytes.NewReader(a)), json.NewDecoder(bytes.NewReader(b))
	leftDecoder.UseNumber()
	rightDecoder.UseNumber()
	return leftDecoder.Decode(&left) == nil && rightDecoder.Decode(&right) == nil && reflect.DeepEqual(left, right)
}

func isPiGentleNpmSource(source string) bool {
	// Pi 0.99 parseSource requires this exact prefix, then applies JavaScript
	// trim to the spec. Leading whitespace before npm: instead denotes a path.
	if !strings.HasPrefix(source, "npm:") {
		return false
	}
	const jsWhitespace = "\t\n\v\f\r \u00a0\u1680\u2000\u2001\u2002\u2003\u2004\u2005\u2006\u2007\u2008\u2009\u200a\u2028\u2029\u202f\u205f\u3000\ufeff"
	spec := strings.Trim(source[len("npm:"):], jsWhitespace)
	if spec == "gentle-engram" {
		return true
	}
	version, ok := strings.CutPrefix(spec, "gentle-engram@")
	// Pi's optional version is .+ (JavaScript dot excludes line terminators).
	return ok && version != "" && !strings.ContainsAny(version, "\r\n\u2028\u2029")
}

// Pi install persists by npm package identity, not by exact version. Protect the
// first matching custom entry before that external command can replace it.
func installPiPackage(settingsPath string) error {
	config, err := readJSONConfig(settingsPath)
	if err != nil {
		return fmt.Errorf("read Pi settings before install: %w", err)
	}
	packages, err := readRawArrayField(config, "packages", settingsPath)
	if err != nil {
		return err
	}
	for index, original := range packages {
		var source string
		var object map[string]json.RawMessage
		isString := json.Unmarshal(original, &source) == nil
		if !isString {
			if json.Unmarshal(original, &object) != nil || json.Unmarshal(object["source"], &source) != nil {
				continue
			}
		}
		if !isPiGentleNpmSource(source) {
			continue
		}
		if (isString && isPiManagedPackage(source)) || source == piGentleEngramPackage {
			break
		}
		expected, err := json.Marshal(piGentleEngramPackage)
		if err != nil {
			return err
		}
		if !isString {
			object["source"] = expected
			expected, err = json.Marshal(object)
			if err != nil {
				return err
			}
		}
		backup, err := os.CreateTemp(filepath.Dir(settingsPath), ".engram-pi-settings-*")
		if err != nil {
			return fmt.Errorf("back up Pi settings before install: %w", err)
		}
		data, marshalErr := json.MarshalIndent(config, "", "  ")
		_, writeErr := backup.Write(data)
		closeErr := backup.Close()
		if err := errors.Join(marshalErr, writeErr, closeErr); err != nil {
			return errors.Join(fmt.Errorf("back up Pi settings: %w", err), os.Remove(backup.Name()))
		}
		_, installErr := runCommand("pi", "install", piGentleEngramPackage)
		latest, readErr := readJSONConfig(settingsPath)
		if readErr == nil {
			var current []json.RawMessage
			current, readErr = readRawArrayField(latest, "packages", settingsPath)
			if readErr == nil {
				switch {
				case index >= len(current):
					readErr = errors.New("pi package position disappeared during install")
				case samePiJSON(current[index], original):
					// No mutation (including the normal pre-persistence failure).
				case samePiJSON(current[index], expected):
					current[index] = original
					latest["packages"], readErr = json.Marshal(current)
					if readErr == nil {
						readErr = writeJSONConfig(settingsPath, latest)
					}
				default:
					readErr = errors.New("pi package changed unexpectedly during install")
				}
			}
		}
		if readErr != nil {
			return errors.Join(installErr, fmt.Errorf("preserve custom Pi package: %w; recovery settings retained at %s", readErr, backup.Name()))
		}
		if err := os.Remove(backup.Name()); err != nil {
			return errors.Join(installErr, fmt.Errorf("remove Pi settings recovery copy %s: %w", backup.Name(), err))
		}
		return installErr
	}
	_, err = runCommand("pi", "install", piGentleEngramPackage)
	return err
}
