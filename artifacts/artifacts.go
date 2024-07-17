package artifacts

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
)

func DownloadArtifacts() {
	// Replace these with your desired values
	outputDir := "output"

	// Create output directory if it doesn't exist
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		fmt.Printf("Error creating output directory: %v", err)
		return
	}

	// Download and extract the artifact
	rethURL := "https://github.com/paradigmxyz/reth/releases/download/v1.0.0/reth-v1.0.0-x86_64-unknown-linux-gnu.tar.gz"
	fmt.Println("Downloading Reth binary")
	if err := downloadArtifact(rethURL, outputDir); err != nil {
		fmt.Printf("Error downloading artifact: %v", err)
		return
	}

	lighthouseURL := "https://github.com/sigp/lighthouse/releases/download/v5.2.1/lighthouse-v5.2.1-x86_64-unknown-linux-gnu.tar.gz"
	fmt.Println("Downloading Lighthouse binary")
	if err := downloadArtifact(lighthouseURL, outputDir); err != nil {
		fmt.Printf("Error downloading artifact: %v", err)
		return
	}
}

func downloadArtifact(url string, outputDir string) error {
	// Download the file
	resp, err := http.Get(url)
	if err != nil {
		return fmt.Errorf("error downloading file: %v", err)
	}
	defer resp.Body.Close()

	// Create a gzip reader
	gzipReader, err := gzip.NewReader(resp.Body)
	if err != nil {
		return fmt.Errorf("error creating gzip reader: %v", err)
	}
	defer gzipReader.Close()

	// Create a tar reader
	tarReader := tar.NewReader(gzipReader)

	// Extract the file
	for {
		header, err := tarReader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("error reading tar: %v", err)
		}

		if header.Typeflag == tar.TypeReg {
			outPath := filepath.Join(outputDir, header.Name)
			outFile, err := os.Create(outPath)
			if err != nil {
				return fmt.Errorf("error creating output file: %v", err)
			}
			defer outFile.Close()

			if _, err := io.Copy(outFile, tarReader); err != nil {
				return fmt.Errorf("error writing output file: %v", err)
			}

			// change permissions
			if err := os.Chmod(outPath, 0755); err != nil {
				return fmt.Errorf("error changing permissions: %v", err)
			}
			break // Assuming there's only one file per repo
		}
	}

	return nil
}
