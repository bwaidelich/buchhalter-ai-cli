package repository

import (
	"buchhalter/lib/parser"
	"buchhalter/lib/vault"
	"bytes"
	"encoding/json"
	"fmt"
	"github.com/spf13/viper"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"time"
)

type Metric struct {
	MetricType    string `json:"type,omitempty"`
	Data          string `json:"data,omitempty"`
	CliVersion    string `json:"cliVersion,omitempty"`
	OicdbVersion  string `json:"oicdbVersion,omitempty"`
	VaultVersion  string `json:"vaultVersion,omitempty"`
	ChromeVersion string `json:"chromeVersion,omitempty"`
	OS            string `json:"os,omitempty"`
}

type RunData []RunDataProvider
type RunDataProvider struct {
	Provider         string  `json:"provider,omitempty"`
	Version          string  `json:"version,omitempty"`
	Status           string  `json:"status,omitempty"`
	LastErrorMessage string  `json:"lastErrorMessage,omitempty"`
	Duration         float64 `json:"duration,omitempty"`
	NewFilesCount    int     `json:"newFilesCount,omitempty"`
}

func updateExists() (bool, error) {
	repositoryUrl := viper.GetString("buchhalter_repository_url")
	currentChecksum := viper.GetString("buchhalter_repository_checksum")
	client := &http.Client{
		Timeout: 10 * time.Second,
	}
	req, err := http.NewRequest("HEAD", repositoryUrl, nil)
	if err != nil {
		return false, err
	}
	req.Header.Set("User-Agent", "buchhalter-cli")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return false, fmt.Errorf("error sending request")
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusOK {
		checksum := resp.Header.Get("x-checksum")
		if checksum != "" {
			if checksum == currentChecksum {
				return false, nil
			}
			return true, nil
		} else {
			return false, fmt.Errorf("update failed with checksum mismatch")
		}
	} else {
		return false, fmt.Errorf("http request failed with status code: %d\n", resp.StatusCode)
	}
}

func UpdateIfAvailable() error {
	repositoryUrl := viper.GetString("buchhalter_repository_url")
	updateExists, err := updateExists()
	if err != nil {
		fmt.Printf("You're offline. Please connect to the internet for using buchhalter-cli")
		os.Exit(1)
	}
	if updateExists {
		client := &http.Client{
			Timeout: 10 * time.Second,
		}
		req, err := http.NewRequest("GET", repositoryUrl, nil)
		if err != nil {
			return fmt.Errorf("error creating request: %s\n", err)
		}
		req.Header.Set("User-Agent", "buchhalter-cli")
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json")
		resp, err := client.Do(req)
		if err != nil {
			return fmt.Errorf("error sending request: %s\n", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode == http.StatusOK {
			out, err := os.Create(filepath.Join(viper.GetString("buchhalter_config_directory"), "oicdb.json"))
			if err != nil {
				return fmt.Errorf("couldn't create oicdb.json file: %s\n", err)
			}
			defer out.Close()
			io.Copy(out, resp.Body)
		} else {
			return fmt.Errorf("http request failed with status code: %d\n", resp.StatusCode)
		}
	}
	return nil
}

func SendMetrics(rd RunData, v string, c string) {
	metricsUrl := viper.GetString("buchhalter_metrics_url")
	rdx, err := json.Marshal(rd)
	md := Metric{
		MetricType:    "runMetrics",
		Data:          string(rdx),
		CliVersion:    v,
		OicdbVersion:  parser.OicdbVersion,
		VaultVersion:  vault.VaultVersion,
		ChromeVersion: c,
		OS:            runtime.GOOS,
	}
	mdj, err := json.Marshal(md)

	client := &http.Client{}
	req, err := http.NewRequest("POST", metricsUrl, bytes.NewBuffer(mdj))
	if err != nil {
		log.Println("Error creating request:", err)
		return
	}
	req.Header.Set("User-Agent", "buchhalter-cli")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		log.Println("Error sending request:", err)
		fmt.Printf("%s", resp)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusOK {
		return
	} else {
		fmt.Printf("HTTP request failed with status code: %d\n", resp.StatusCode)
		return
	}
}
