package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestFetchKubernetesVersions_ValidURL_ReturnsData(t *testing.T) {
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`[{"cycle":"v1.24","releaseDate":"2022-05-03","eol":"2023-10-03","latest":"v1.24.0"}]`))
	}))
	defer mockServer.Close()

	versions, err := fetchKubernetesVersions(mockServer.URL)
	if err != nil {
		t.Fatalf("Expected no error, got %v", err)
	}
	if len(versions) != 1 {
		t.Fatalf("Expected 1 version, got %d", len(versions))
	}
	if versions[0].Cycle != "v1.24" {
		t.Fatalf("Expected cycle v1.24, got %s", versions[0].Cycle)
	}
}

func TestFetchKubernetesVersions_InvalidURL_ReturnsError(t *testing.T) {
	url := "https://invalid.url"
	_, err := fetchKubernetesVersions(url)
	if err == nil {
		t.Fatal("Expected an error, got nil")
	}
}

func TestUpdateVersionsBlock_ValidInput_UpdatesBlock(t *testing.T) {
	versions := []KubernetesVersion{
		{"v1.24", "2022-05-03", "2034-10-03", "v1.24.0"},
		{"v1.25", "2022-08-02", "2036-01-02", "v1.25.0"},
	}
	versionBlock := []string{
		"- v1.22 # EOL 2023-10-03",
		"- v1.23 # EOL 2023-01-01",
	}
	result, err := updateVersionsBlock(versions, versionBlock)
	if err != nil {
		t.Fatalf("Expected no error, got %v", err)
	}
	if len(result) != 2 {
		t.Fatalf("Expected 2 lines, got %d", len(result))
	}
}

func TestUpdateVersionsBlock_EmptyInput_ReturnsEmpty(t *testing.T) {
	versions := []KubernetesVersion{}
	versionBlock := []string{}
	result, err := updateVersionsBlock(versions, versionBlock)
	if err != nil {
		t.Fatalf("Expected no error, got %v", err)
	}
	if len(result) != 0 {
		t.Fatalf("Expected empty result, got %v", result)
	}
}

func TestUpdateK8sVersion_ValidFile_UpdatesFile(t *testing.T) {
	filePath := "test.yaml"
	_ = os.WriteFile(filePath, []byte("k8s-version:\n- v1.23 # EOL 2023-01-01\n\n"), 0644)
	defer os.Remove(filePath)

	versions := []KubernetesVersion{
		{"v1.24", "2022-05-03", "2033-10-03", "v1.24.0"},
	}

	err := updateK8sVersion(filePath, versions)
	if err != nil {
		t.Fatalf("Expected no error, got %v", err)
	}

	content, _ := os.ReadFile(filePath)
	if !strings.Contains(string(content), "v1.24") {
		t.Fatalf("Expected updated content with v1.24, got %s", string(content))
	}
}

func TestUpdateK8sVersion_InvalidFile_ReturnsError(t *testing.T) {
	err := updateK8sVersion("nonexistent.yaml", nil)
	if err == nil {
		t.Fatal("Expected an error, got nil")
	}
}

func TestUpdateVersionsBlock_EOLDateInPast_ExcludesVersion(t *testing.T) {
	versions := []KubernetesVersion{
		{"v1.24", "2022-05-03", "2020-01-01", "v1.24.0"},
	}
	versionBlock := []string{
		"- v1.24 # EOL 2020-01-01",
	}
	result, err := updateVersionsBlock(versions, versionBlock)
	if err != nil {
		t.Fatalf("Expected no error, got %v", err)
	}
	if len(result) != 0 {
		t.Fatalf("Expected empty result, got %v", result)
	}
}
