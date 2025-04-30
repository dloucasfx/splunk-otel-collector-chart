// Copyright Splunk Inc.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

var debug bool

const (
	EndOfLifeURL string = "https://endoflife.date/api/kubernetes.json"
	DockerHubURL string = "https://hub.docker.com/v2/repositories/kindest/node/tags?page_size=1&page=1&ordering=last_updated&name="
	MiniKubeURL  string = "https://raw.githubusercontent.com/kubernetes/minikube/master/pkg/minikube/constants/constants_kubernetes_versions.go"
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

// getSupportedKubernetesVersions returns only the supported Kubernetes versions
// by checking the EOL date of the collected versions.
func getSupportedKubernetesVersions() ([]KubernetesVersion, error) {
	body, err := getRequest(EndOfLifeURL)
	if err != nil {
		return nil, fmt.Errorf("failed to get k8s versions: %w", err)
	}
	var kubernetesVersions, supportedKubernetesVersions []KubernetesVersion
	if err := json.Unmarshal(body, &kubernetesVersions); err != nil {
		return nil, fmt.Errorf("failed to unmarshal JSON: %w", err)
	}

	now := time.Now()
	for _, kubernetesVersion := range kubernetesVersions {
		eolDate, err := time.Parse("2006-01-02", kubernetesVersion.EOLDate)
		if err != nil {
			return nil, fmt.Errorf("error parsing date: %w", err)
		}
		if eolDate.After(now) {
			supportedKubernetesVersions = append(supportedKubernetesVersions, kubernetesVersion)
		} else {
			logDebug("Skipping version %s, EOL date %s", kubernetesVersion.Cycle, kubernetesVersion.EOLDate)
		}
	}
	return supportedKubernetesVersions, nil
}

func getLatestSupportedMinikubeVersions(k8sVersions []KubernetesVersion) ([]string, error) {
	body, err := getRequest(MiniKubeURL)
	if err != nil {
		return nil, fmt.Errorf("failed to get minikube versions: %w", err)
	}

	// Extract the slice using a regular expression
	re := regexp.MustCompile(`ValidKubernetesVersions = \[\]string{([^}]*)}`)
	matches := re.FindStringSubmatch(string(body))
	if len(matches) < 2 {
		return nil, fmt.Errorf("minikube, failed to find the Kubernetes versions slice")
	}

	// Parse and cleanup the slice values
	versions := strings.Split(strings.ReplaceAll(strings.ReplaceAll(matches[1], "\n", ""), `"`, ""), ",")

	logDebug("Found the following minikube versions: %s", versions)

	var latestMinikubeVersions []string
	// the minikube version slice is sorted, break when first cycle match is found
	for _, k8sVersion := range k8sVersions {
		for _, version := range versions {
			if strings.Contains(version, k8sVersion.Cycle) {
				latestMinikubeVersions = append(latestMinikubeVersions, strings.TrimSpace(version))
				break
			}
		}
	}

	return latestMinikubeVersions, nil
}

// getLatestSupportedKindImages iterates through the K8s supported versions and find the latest kind
// tag that supports that version
func getLatestSupportedKindImages(k8sVersions []KubernetesVersion) ([]string, error) {
	var supportedKindVersions []string
	for _, k8sVersion := range k8sVersions {
		tag := k8sVersion.Latest
		for {
			exists, err := imageTagExists(tag)
			if err != nil {
				return supportedKindVersions, fmt.Errorf("failed to check image tag existence: %w", err)
			}
			if exists {
				supportedKindVersions = append(supportedKindVersions, "v"+tag)
				break
			}
			tag, err = decrementMinorMinorVersion(tag)
			if err != nil {
				// It's possible that kind still does not have a tag for new versions, break the loop and
				// process other k8s versions
				if strings.Contains(err.Error(), "minor version cannot be decremented below 0") {
					logDebug("No kind image found for k8s version %s", k8sVersion.Cycle)
					break
				}
				return supportedKindVersions, fmt.Errorf("failed to decrement k8sVersion: %w", err)
			}
		}
	}
	return supportedKindVersions, nil
}

func imageTagExists(tag string) (bool, error) {
	body, err := getRequest(DockerHubURL + tag)
	if err != nil {
		return false, fmt.Errorf("failed to get image tag: %w", err)
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

func updateMatrixFile(filePath string, kindVersions []string, minikubeVersions []string) error {
	content, err := os.ReadFile(filePath)
	if err != nil {
		return fmt.Errorf("failed to read file: %w", err)
	}

	var testMatrix map[string]map[string][]string
	if err := json.Unmarshal(content, &testMatrix); err != nil {
		return fmt.Errorf("failed to unmarshal JSON: %w", err)
	}

	for _, value := range testMatrix {
		if len(kindVersions) > 0 && value["k8s-kind-version"] != nil {
			value["k8s-kind-version"] = kindVersions
		} else if len(minikubeVersions) > 0 && value["k8s-minikube-version"] != nil {
			value["k8s-minikube-version"] = minikubeVersions
		}
	}
	// Marshal the updated test matrix back to JSON
	updatedContent, err := json.MarshalIndent(testMatrix, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal updated JSON: %w", err)
	}

	if err := os.WriteFile(filePath, updatedContent, 0644); err != nil {
		return fmt.Errorf("failed to write updated file: %w", err)
	}
	return nil
}

func sortVersions(versions []string) {
	sort.Slice(versions, func(i, j int) bool {
		vi := strings.Split(versions[i][1:], ".") // Remove "v" and split by "."
		vj := strings.Split(versions[j][1:], ".")

		for k := 0; k < len(vi) && k < len(vj); k++ {
			if vi[k] != vj[k] {
				return vi[k] > vj[k] // Sort in descending order
			}
		}
		return len(vi) > len(vj)
	})
}

func getRequest(url string) ([]byte, error) {
	resp, err := http.Get(url)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch URL: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}
	return body, nil
}

func logDebug(format string, v ...interface{}) {
	if debug {
		log.Printf(format, v...)
	}
}

func main() {
	// setup logging
	flag.BoolVar(&debug, "debug", false, "Enable debug logging")
	flag.Parse()
	log.SetOutput(os.Stdout)
	log.SetFlags(log.LstdFlags | log.Lshortfile)

	k8sVersions, err := getSupportedKubernetesVersions()
	if err != nil || len(k8sVersions) == 0 {
		log.Println("Failed to get k8s versions: ", err)
		os.Exit(1) // Exit with code 1 on failure
	}
	logDebug("Found supported k8s versions %v", k8sVersions)

	var errs error

	kindVersions, err := getLatestSupportedKindImages(k8sVersions)
	if err != nil {
		errs = errors.Join(errs, fmt.Errorf("failed to get kind images: %w", err))
	}
	if len(kindVersions) > 0 {
		sortVersions(kindVersions)
		logDebug("Found supported kind images: %v", kindVersions)
	}

	minikubeVersions, err := getLatestSupportedMinikubeVersions(k8sVersions)
	if err != nil {
		errs = errors.Join(errs, fmt.Errorf("failed to get minikube versions: %w", err))
	}
	if len(minikubeVersions) > 0 {
		logDebug("Found supported minikube versions: %v", minikubeVersions)
	}

	if len(kindVersions) == 0 && len(minikubeVersions) == 0 || errs != nil {
		log.Println("No supported versions found or errors occurred: ", errs)
		os.Exit(2)
	}

	path := "tools/k8s_versions/test-matrix.json"
	currentDir, err := os.Getwd()
	if err != nil {
		log.Println("Failed to get current directory: ", err)
		os.Exit(1)
	}
	path = filepath.Join(currentDir, filepath.Clean(path))
	err = updateMatrixFile(path, kindVersions, minikubeVersions)
	if err != nil {
		log.Println("Failed to update matrix file: ", err)
		os.Exit(1)
	}
	os.Exit(0)
}
