package modrinth

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	modrinthApi "codeberg.org/jmansfield/go-modrinth/modrinth"
	"github.com/codecraft3r/packwiz/core"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

// ModrinthIndexFile represents the structure of modrinth.index.json in .mrpack files
type ModrinthIndexFile struct {
	FormatVersion int                    `json:"formatVersion"`
	Game          string                 `json:"game"`
	VersionID     string                 `json:"versionId"`
	Name          string                 `json:"name"`
	Summary       string                 `json:"summary,omitempty"`
	Files         []ModrinthIndexFileRef `json:"files"`
	Dependencies  map[string]string      `json:"dependencies"`
}

// ModrinthIndexFileRef represents a file reference in the modrinth index
type ModrinthIndexFileRef struct {
	Path      string            `json:"path"`
	Hashes    map[string]string `json:"hashes"`
	Env       *FileEnv          `json:"env,omitempty"`
	Downloads []string          `json:"downloads"`
	FileSize  int               `json:"fileSize"`
}

// FileEnv represents the environment requirements for a file
type FileEnv struct {
	Client string `json:"client"`
	Server string `json:"server"`
}

// HashRequest represents the request structure for the Modrinth API hash lookup
type HashRequest struct {
	Hashes    []string `json:"hashes"`
	Algorithm string   `json:"algorithm"`
}

// HashResponse represents a single item in the hash lookup response
type HashResponse struct {
	ID        string `json:"id"`
	ProjectID string `json:"project_id"`
}

// importCmd represents the import command
var importCmd = &cobra.Command{
	Use:   "import [mrpack file path]",
	Short: "Import a Modrinth modpack from a .mrpack file",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		mrpackFilePath := args[0]

		// Check if the file exists and is a zip file
		r, err := zip.OpenReader(mrpackFilePath)
		if err != nil {
			fmt.Printf("Failed to open .mrpack file: %v\n", err)
			os.Exit(1)
		}
		defer r.Close()

		// Load pack
		pack, err := core.LoadPack()
		if err != nil {
			fmt.Println("Failed to load existing pack, creating a new one...")
			// For simplicity, we'll require an existing pack for now
			// In a full implementation, we could create a new pack based on mrpack metadata
			fmt.Println("Please run 'packwiz init' first to create a pack")
			os.Exit(1)
		}

		index, err := pack.LoadIndex()
		if err != nil {
			fmt.Printf("Failed to load pack index: %v\n", err)
			os.Exit(1)
		}

		// Extract and parse modrinth.index.json
		modrinthIndex, err := extractModrinthIndex(r)
		if err != nil {
			fmt.Printf("Failed to extract modrinth index: %v\n", err)
			os.Exit(1)
		}

		fmt.Printf("Importing modpack: %s\n", modrinthIndex.Name)
		if modrinthIndex.Summary != "" {
			fmt.Printf("Description: %s\n", modrinthIndex.Summary)
		}

		// Get SHA512 hashes from the files
		var hashes []string
		for _, file := range modrinthIndex.Files {
			if sha512, ok := file.Hashes["sha512"]; ok {
				hashes = append(hashes, sha512)
			}
		}

		if len(hashes) == 0 {
			fmt.Println("No files with SHA512 hashes found in the modpack")
			return
		}

		// Look up version IDs from hashes
		versionMap, err := lookupVersionsByHash(hashes)
		if err != nil {
			fmt.Printf("Failed to lookup versions by hash: %v\n", err)
			os.Exit(1)
		}

		fmt.Printf("Found %d mods to install\n", len(versionMap))

		// Get list of already installed Modrinth projects to avoid duplicates
		installedProjects := getInstalledProjectIDs(&index)
		fmt.Printf("Found %d already installed Modrinth mods\n", len(installedProjects))

		// Install each mod
		successCount := 0
		skippedCount := 0
		totalMods := len(versionMap)

		// Set up crash recovery - save progress periodically and on exit
		saveProgress := func() {
			if successCount > 0 {
				fmt.Printf("Saving progress (%d mods installed)...\n", successCount)
				if writeErr := index.Write(); writeErr != nil {
					fmt.Printf("Warning: Failed to save progress to index: %v\n", writeErr)
				} else {
					if hashErr := pack.UpdateIndexHash(); hashErr != nil {
						fmt.Printf("Warning: Failed to update pack hash: %v\n", hashErr)
					} else {
						if packErr := pack.Write(); packErr != nil {
							fmt.Printf("Warning: Failed to save pack: %v\n", packErr)
						}
					}
				}
			}
		}

		// Set up signal handling to catch Ctrl+C and save progress
		sigChan := make(chan os.Signal, 1)
		signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

		// Channel to communicate completion
		doneChan := make(chan bool, 1)

		// Goroutine to handle signals
		go func() {
			select {
			case sig := <-sigChan:
				fmt.Printf("\nReceived signal %v, saving progress and exiting...\n", sig)
				saveProgress()
				fmt.Printf("Import interrupted. Progress saved: %d installed, %d skipped\n", successCount, skippedCount)
				os.Exit(0)
			case <-doneChan:
				// Normal completion, exit the goroutine
				return
			}
		}()

		// Defer cleanup to ensure progress is saved on any exit (including crashes/interrupts)
		defer func() {
			if r := recover(); r != nil {
				fmt.Printf("\nImport interrupted! Attempting to save progress...\n")
				saveProgress()
				fmt.Printf("Recovered from crash: %v\n", r)
				fmt.Printf("Import partially completed: %d installed, %d skipped\n", successCount, skippedCount)
			}
		}()

		processedCount := 0
		for hash, versionInfo := range versionMap {
			processedCount++

			// Find the corresponding file info
			var fileRef *ModrinthIndexFileRef
			for _, file := range modrinthIndex.Files {
				if fileSha512, ok := file.Hashes["sha512"]; ok && fileSha512 == hash {
					fileRef = &file
					break
				}
			}

			if fileRef == nil {
				fmt.Printf("Warning: Could not find file info for hash %s\n", hash[:8])
				continue
			}

			// Check if this project is already installed
			projectAlreadyInstalled := false
			for _, installedID := range installedProjects {
				if installedID == versionInfo.ProjectID {
					projectAlreadyInstalled = true
					break
				}
			}

			if projectAlreadyInstalled {
				fmt.Printf("Skipping already installed project (project ID: %s)\n", versionInfo.ProjectID)
				skippedCount++
				continue
			}

			// Get project information from API to determine accurate side information (with rate limit handling)
			project, err := getProjectWithRateLimit(versionInfo.ProjectID)
			if err != nil {
				fmt.Printf("Failed to get project info for version ID %s: %v\n", versionInfo.ID, err)
				continue
			}

			// Use API data for side determination instead of mrpack env data
			side := getSide(project)
			if side == "" {
				fmt.Printf("Warning: Project %s doesn't have a supported side; assuming universal. Server: %s Client: %s\n",
					*project.Title, *project.ServerSide, *project.ClientSide)
				side = core.UniversalSide
			}

			fmt.Printf("Installing mod %s (%d/%d) (version ID: %s) with side: %s...\n",
				*project.Title, processedCount, totalMods, versionInfo.ID, side)

			// Install the mod with API-determined side information
			err = installVersionByIdWithSide(versionInfo.ID, "", side, pack, &index)
			if err != nil {
				fmt.Printf("Failed to install mod %s with version ID %s: %v\n", *project.Title, versionInfo.ID, err)
			} else {
				successCount++

				// Save progress every 10 successful installations to minimize loss on crash
				if successCount%10 == 0 {
					fmt.Printf("Checkpoint: Saving progress after %d installations...\n", successCount)
					saveProgress()
				}
			}
		}

		// Signal completion to stop the signal handler
		close(doneChan)

		fmt.Printf("Import summary: %d installed, %d skipped (already installed), %d failed\n",
			successCount, skippedCount, len(versionMap)-successCount-skippedCount)

		// Copy overrides if they exist
		err = copyOverrides(r, &index)
		if err != nil {
			fmt.Printf("Warning: Failed to copy overrides: %v\n", err)
		}

		// Write the updated index
		err = index.Write()
		if err != nil {
			fmt.Printf("Failed to write index: %v\n", err)
			os.Exit(1)
		}

		// Update pack hash
		err = pack.UpdateIndexHash()
		if err != nil {
			fmt.Printf("Failed to update pack hash: %v\n", err)
			os.Exit(1)
		}

		err = pack.Write()
		if err != nil {
			fmt.Printf("Failed to write pack: %v\n", err)
			os.Exit(1)
		}

		fmt.Println("Import completed!")
		failedCount := len(versionMap) - successCount - skippedCount
		if failedCount > 0 {
			fmt.Printf("%d mods failed to install. You may need to install them manually.\n", failedCount)
		}
	},
}

// extractModrinthIndex reads and parses the modrinth.index.json file from the .mrpack
func extractModrinthIndex(r *zip.ReadCloser) (*ModrinthIndexFile, error) {
	for _, f := range r.File {
		if f.Name == "modrinth.index.json" {
			rc, err := f.Open()
			if err != nil {
				return nil, fmt.Errorf("failed to open modrinth.index.json: %v", err)
			}
			defer rc.Close()

			data, err := io.ReadAll(rc)
			if err != nil {
				return nil, fmt.Errorf("failed to read modrinth.index.json: %v", err)
			}

			var modrinthIndex ModrinthIndexFile
			err = json.Unmarshal(data, &modrinthIndex)
			if err != nil {
				return nil, fmt.Errorf("failed to parse modrinth.index.json: %v", err)
			}

			return &modrinthIndex, nil
		}
	}
	return nil, fmt.Errorf("modrinth.index.json not found in .mrpack file")
}

// lookupVersionsByHash queries the Modrinth API to get version information from file hashes
func lookupVersionsByHash(hashes []string) (map[string]HashResponse, error) {
	hashRequest := HashRequest{
		Hashes:    hashes,
		Algorithm: "sha512",
	}

	jsonData, err := json.Marshal(hashRequest)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal hash request: %v", err)
	}

	resp, err := http.Post("https://api.modrinth.com/v2/version_files", "application/json", bytes.NewBuffer(jsonData))
	if err != nil {
		return nil, fmt.Errorf("failed to make API request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("API request failed with status: %s", resp.Status)
	}

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %v", err)
	}

	var responseData map[string]HashResponse
	err = json.Unmarshal(bodyBytes, &responseData)
	if err != nil {
		return nil, fmt.Errorf("failed to parse response JSON: %v", err)
	}

	return responseData, nil
}

// getProjectWithRateLimit gets project information with rate limit handling
func getProjectWithRateLimit(projectID string) (*modrinthApi.Project, error) {
	maxRetries := 3

	for attempt := 0; attempt < maxRetries; attempt++ {
		project, err := mrDefaultClient.Projects.Get(projectID)
		if err == nil {
			return project, nil
		}

		// Check if this is a rate limit error (status 429 or rate limit in error message)
		errorMsg := err.Error()
		isRateLimit := false

		// Check for common rate limit indicators
		if containsAny(errorMsg, []string{"429", "rate limit", "Rate limit", "too many requests", "Too Many Requests"}) {
			isRateLimit = true
		}

		if isRateLimit && attempt < maxRetries-1 {
			// Wait 60 seconds for rate limit reset (Modrinth resets every minute)
			waitTime := 60 * time.Second
			fmt.Printf("Rate limited, waiting %v before retry %d/%d...\n", waitTime, attempt+2, maxRetries)
			time.Sleep(waitTime)
			continue
		}

		// For non-rate-limit errors or final attempt, return the error
		return nil, err
	}

	return nil, fmt.Errorf("failed to get project after %d retries", maxRetries)
}

// containsAny checks if a string contains any of the given substrings
func containsAny(s string, substrings []string) bool {
	for _, substr := range substrings {
		if len(s) >= len(substr) {
			for i := 0; i <= len(s)-len(substr); i++ {
				if s[i:i+len(substr)] == substr {
					return true
				}
			}
		}
	}
	return false
}

// copyOverrides copies override files from the .mrpack to the pack directory
func copyOverrides(r *zip.ReadCloser, index *core.Index) error {
	overridesCopied := 0

	for _, f := range r.File {
		// Check if file is in overrides directory
		if len(f.Name) > 10 && f.Name[:10] == "overrides/" {
			relativePath := f.Name[10:] // Remove "overrides/" prefix

			if f.FileInfo().IsDir() {
				// Create directory
				destPath := index.ResolveIndexPath(relativePath)
				err := os.MkdirAll(destPath, f.FileInfo().Mode())
				if err != nil {
					return fmt.Errorf("failed to create directory %s: %v", destPath, err)
				}
				continue
			}

			// Copy file
			rc, err := f.Open()
			if err != nil {
				return fmt.Errorf("failed to open override file %s: %v", f.Name, err)
			}

			destPath := index.ResolveIndexPath(relativePath)

			// Ensure parent directory exists
			err = os.MkdirAll(filepath.Dir(destPath), 0755)
			if err != nil {
				rc.Close()
				return fmt.Errorf("failed to create parent directory for %s: %v", destPath, err)
			}

			destFile, err := os.Create(destPath)
			if err != nil {
				rc.Close()
				return fmt.Errorf("failed to create override file %s: %v", destPath, err)
			}

			_, err = io.Copy(destFile, rc)
			rc.Close()
			destFile.Close()

			if err != nil {
				return fmt.Errorf("failed to copy override file %s: %v", relativePath, err)
			}

			overridesCopied++
		}
	}

	if overridesCopied > 0 {
		fmt.Printf("Copied %d override files\n", overridesCopied)
	}

	return nil
}

// installVersionByIdWithSide installs a mod version with a specific side override
func installVersionByIdWithSide(versionId string, versionFilename string, side string, pack core.Pack, index *core.Index) error {
	// Get version information from Modrinth API
	version, err := mrDefaultClient.Versions.Get(versionId)
	if err != nil {
		return fmt.Errorf("failed to get version info: %v", err)
	}

	// Get project information
	project, err := mrDefaultClient.Projects.Get(*version.ProjectID)
	if err != nil {
		return fmt.Errorf("failed to get project info: %v", err)
	}

	// Install the version with custom side
	return installVersionWithSide(project, version, versionFilename, side, pack, index)
}

// installVersionWithSide installs a version with a custom side override
func installVersionWithSide(project *modrinthApi.Project, version *modrinthApi.Version, versionFilename string, customSide string, pack core.Pack, index *core.Index) error {
	// Find the appropriate file
	var file *modrinthApi.File
	if versionFilename == "" {
		file = findPrimaryFile(version, pack.GetCompatibleLoaders())
		if file == nil {
			return fmt.Errorf("no compatible files found for this version")
		}
	} else {
		for _, f := range version.Files {
			if *f.Filename == versionFilename {
				file = f
				break
			}
		}
		if file == nil {
			return fmt.Errorf("file with name %s not found", versionFilename)
		}
	}

	// Create file metadata with custom side
	return createFileMetaWithSide(project, version, file, customSide, pack, index)
}

// createFileMetaWithSide creates mod metadata with a custom side override
func createFileMetaWithSide(project *modrinthApi.Project, version *modrinthApi.Version, file *modrinthApi.File, customSide string, pack core.Pack, index *core.Index) error {
	updateMap := make(map[string]map[string]interface{})

	var err error
	updateMap["modrinth"], err = mrUpdateData{
		ProjectID:        *project.ID,
		InstalledVersion: *version.ID,
	}.ToMap()
	if err != nil {
		return err
	}

	algorithm, hash := getBestHash(file)
	if algorithm == "" {
		return errors.New("file doesn't have a hash")
	}

	modMeta := core.Mod{
		Name:     *project.Title,
		FileName: *file.Filename,
		Side:     customSide, // Use the custom side instead of detecting from project
		Download: core.ModDownload{
			URL:                     *file.URL,
			HashFormat:              algorithm,
			Hash:                    hash,
			DisabledClientPlatforms: []string{}, // Default empty for imports
		},
		Update: updateMap,
	}
	var path string
	folder := viper.GetString("meta-folder")
	if folder == "" {
		folder, err = getProjectTypeFolder(*project.ProjectType, version.Loaders, pack.GetCompatibleLoaders())
		if err != nil {
			return err
		}
	}
	if project.Slug != nil {
		path = modMeta.SetMetaPath(filepath.Join(viper.GetString("meta-folder-base"), folder, *project.Slug+core.MetaExtension))
	} else {
		path = modMeta.SetMetaPath(filepath.Join(viper.GetString("meta-folder-base"), folder, core.SlugifyName(*project.Title)+core.MetaExtension))
	}

	format, hash, err := modMeta.Write()
	if err != nil {
		return err
	}
	return index.RefreshFileWithHash(path, format, hash, true)
}

// findPrimaryFile finds the primary file from a version, preferring primary files
func findPrimaryFile(version *modrinthApi.Version, compatibleLoaders []string) *modrinthApi.File {
	if len(version.Files) == 0 {
		return nil
	}

	// First try to find the primary file
	for _, file := range version.Files {
		if file.Primary != nil && *file.Primary {
			return file
		}
	}

	// If no primary file, return the first file
	return version.Files[0]
}

func init() {
	modrinthCmd.AddCommand(importCmd)
}
