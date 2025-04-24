// Copyright Splunk Inc.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type KubernetesVersion struct {
	Cycle       string `json:"cycle"`
	ReleaseDate string `json:"releaseDate"`
	EOLDate     string `json:"eol"`
	Latest      string `json:"latest"`
}

type DockerImage struct {
	Count int `json:"count"`
}

func fetchKubernetesVersions(url string) ([]KubernetesVersion, error) {
	resp, err := http.Get(url)
	if err != nil {
		return nil, fmt.Errorf("failed getting k8s versions: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	var kubernetesVersions []KubernetesVersion
	if err := json.Unmarshal(body, &kubernetesVersions); err != nil {
		return nil, fmt.Errorf("failed to unmarshal JSON: %w", err)
	}

	return kubernetesVersions, nil
}

// yaml parsing does not preserve the original format, mainly blank lines.
// This function reads the raw yaml file, locate and update the k8s-version block
func updateK8sVersion(filePath string, kubernetesVersions []KubernetesVersion) error {
	file, err := os.Open(filePath)
	if err != nil {
		return fmt.Errorf("failed to open file: %w", err)
	}
	defer file.Close()

	var output []string
	var k8sVersionBlock []string
	var inK8sVersionBlock bool

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Text()
		trimmed := strings.TrimSpace(line)

		// Locate the k8s-version block needed to update
		if strings.HasPrefix(trimmed, "k8s-version:") || strings.HasPrefix(trimmed, "kubernetes_version:") {
			inK8sVersionBlock = true
			output = append(output, line)
			continue
		}
		// if we found the k8s version array, extract all versions and pass them to updateVersionsBlock
		if inK8sVersionBlock {
			if strings.HasPrefix(trimmed, "-") || strings.HasPrefix(trimmed, "#") {
				k8sVersionBlock = append(k8sVersionBlock, line)
				continue
			} else if trimmed == "" || !strings.HasPrefix(trimmed, "-") {
				updatedVerBlock, err := updateVersionsBlock(kubernetesVersions, k8sVersionBlock)
				if err != nil {
					return err
				}
				output = append(output, append(updatedVerBlock, line)...)
				inK8sVersionBlock = false
				continue
			}
		}
		output = append(output, line)
	}

	if err := scanner.Err(); err != nil {
		return fmt.Errorf("failed to read file: %w", err)
	}

	if err := os.WriteFile(filePath, []byte(strings.Join(output, "\n")+"\n"), 0644); err != nil {
		return fmt.Errorf("failed to write updated file: %w", err)
	}
	return nil
}

func updateVersionsBlock(versions []KubernetesVersion, versionBlock []string) ([]string, error) {
	var output []string
	now := time.Now()
	const layout = "2006-01-02"
	versionRE := regexp.MustCompile("\\d+.\\d+.\\d+")

	for _, version := range versions {
		eolDate, err := time.Parse(layout, version.EOLDate)
		if err != nil {
			return nil, fmt.Errorf("error parsing date: %w", err)
		}

		for i, line := range versionBlock {
			trimmed := strings.TrimSpace(line)
			tag := ""
			if strings.HasPrefix(trimmed, fmt.Sprintf("- v%s", version.Cycle)) {
				if eolDate.After(now) {
					tag, err = getAvailableKindImage(version.Latest)
					if err != nil {
						return nil, fmt.Errorf("failed to get available kind image: %w", err)
					}
					line = versionRE.ReplaceAllString(line, tag)
					output = append(output, line)
				}
				break
			} else if i == len(versionBlock)-1 && eolDate.After(now) {
				// Add the new version at the end of the list
				tag, err = getAvailableKindImage(version.Latest)
				if err != nil {
					return nil, fmt.Errorf("failed to get available kind image: %w", err)
				}
				output = append(output, fmt.Sprintf("%s- v%s # EOL %s", strings.Repeat(" ", len(line)-len(trimmed)), tag, version.EOLDate))
			}
		}
	}
	return output, nil
}

func getAvailableKindImage(tag string) (string, error) {
	for {
		exists, err := imageTagExists(tag)
		if err != nil {
			return "", fmt.Errorf("failed to check image tag existence: %w", err)
		}
		if exists {
			return tag, nil
		}

		tag, err = decrementMinorMinorVersion(tag)
		if err != nil {
			return "", fmt.Errorf("failed to decrement version: %w", err)
		}
	}
}

func imageTagExists(tag string) (bool, error) {
	url := "https://hub.docker.com/v2/repositories/kindest/node/tags?page_size=1&page=1&ordering=last_updated&name="
	resp, err := http.Get(url + tag)
	if err != nil {
		return false, fmt.Errorf("failed getting k8s versions: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return false, fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return false, fmt.Errorf("failed to read response body: %w", err)
	}

	var kindImage DockerImage
	if err := json.Unmarshal(body, &kindImage); err != nil {
		return false, fmt.Errorf("failed to unmarshal JSON: %w", err)
	}

	if kindImage.Count > 0 {
		return true, nil
	}
	return false, nil
}

func decrementMinorMinorVersion(version string) (string, error) {
	parts := strings.Split(version, ".")
	if len(parts) < 3 {
		return "", fmt.Errorf("version does not have a minor version: %s", version)
	}

	minor, err := strconv.Atoi(parts[2])
	if err != nil {
		return "", fmt.Errorf("invalid minor version: %s", parts[1])
	}

	if minor == 0 {
		return "", fmt.Errorf("minor version cannot be decremented below 0")
	}

	parts[2] = strconv.Itoa(minor - 1)
	return strings.Join(parts, "."), nil
}

func main() {
	url := "https://endoflife.date/api/kubernetes.json"
	k8sVersions, err := fetchKubernetesVersions(url)
	if err != nil {
		fmt.Println("Failed to get k8s versions:", err)
		os.Exit(1) // Exit with code 1 on failure
	}

	filesToUpdate := []string{".github/workflows/functional_test.yaml", ".github/workflows/functional_test_v2.yaml"}
	currentDir, err := os.Getwd()
	if err != nil {
		fmt.Println("Failed to get current directory:", err)
		os.Exit(1)
	}

	var partialFailure bool
	for _, filePath := range filesToUpdate {
		filePath = filepath.Join(currentDir, filepath.Clean(filePath))
		if err := updateK8sVersion(filePath, k8sVersions); err != nil {
			fmt.Println("Error:", err)
			partialFailure = true
		}
	}

	if partialFailure {
		os.Exit(2)
	}

	os.Exit(0)
}
