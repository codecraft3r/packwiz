package modrinth

import (
	"archive/zip"
	"fmt"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/codecraft3r/packwiz/core"
	"github.com/spf13/cobra"
	"github.com/vbauerster/mpb/v4"
	"github.com/vbauerster/mpb/v4/decor"
)

// diffCmd represents the diff command
var diffCmd = &cobra.Command{
	Use:   "diff [mrpack file path]",
	Short: "Compare current pack with a Modrinth modpack (.mrpack)",
	Long: `Compare shows the differences between your current packwiz pack and a Modrinth modpack:
- Mods that are in the mrpack but not in your pack (missing)
- Mods that are in your pack but not in the mrpack (extra)  
- Mods that are in both but have different versions (different)
- Summary of total differences`,
	Args: cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		mrpackFilePath := args[0]

		// Load current pack
		pack, err := core.LoadPack()
		if err != nil {
			fmt.Printf("Failed to load current pack: %v\n", err)
			os.Exit(1)
		}

		index, err := pack.LoadIndex()
		if err != nil {
			fmt.Printf("Failed to load pack index: %v\n", err)
			os.Exit(1)
		}

		fmt.Printf("Comparing pack '%s' with mrpack file: %s\n\n", pack.Name, mrpackFilePath)

		// Create progress container
		progressContainer := mpb.New()

		// Parse the mrpack file
		fmt.Println("Parsing mrpack file...")
		mrpackData, err := parseMrpackFile(mrpackFilePath)
		if err != nil {
			fmt.Printf("Failed to parse mrpack file: %v\n", err)
			os.Exit(1)
		}

		fmt.Printf("Mrpack: %s\n", mrpackData.Name)
		if mrpackData.Summary != "" {
			fmt.Printf("Description: %s\n", mrpackData.Summary)
		}
		fmt.Println()

		// Get current pack's Modrinth mods and other sources
		fmt.Println("Analyzing current pack...")
		currentMods, otherSourceMods, err := getCurrentModrinthMods(&index, progressContainer)
		if err != nil {
			fmt.Printf("Failed to get current Modrinth mods: %v\n", err)
			os.Exit(1)
		}

		// Get mrpack mods with project info
		fmt.Println("Fetching mrpack mod information...")
		mrpackMods, err := getMrpackModsWithInfo(mrpackData, progressContainer)
		if err != nil {
			fmt.Printf("Failed to get mrpack mod info: %v\n", err)
			os.Exit(1)
		}

		// Wait for all progress bars to complete
		progressContainer.Wait()
		fmt.Println() // Add spacing after progress bars

		// Compare the mods
		fmt.Println("Comparing mods...")
		missing, extra, different := compareMods(currentMods, mrpackMods)

		// Display results
		displayDifferences(missing, extra, different)

		// Summary
		totalDiffs := len(missing) + len(extra) + len(different)
		totalCurrentMods := otherSourceMods.Modrinth + otherSourceMods.CurseForge + otherSourceMods.URL + otherSourceMods.Other

		fmt.Printf("\n=== Summary ===\n")
		fmt.Printf("Current pack mods by source:\n")
		fmt.Printf("- Modrinth: %d\n", otherSourceMods.Modrinth)
		if otherSourceMods.CurseForge > 0 {
			fmt.Printf("- CurseForge: %d\n", otherSourceMods.CurseForge)
		}
		if otherSourceMods.URL > 0 {
			fmt.Printf("- Direct URLs: %d\n", otherSourceMods.URL)
		}
		if otherSourceMods.Other > 0 {
			fmt.Printf("- Other sources: %d\n", otherSourceMods.Other)
		}
		fmt.Printf("Total: %d mods\n", totalCurrentMods)
		fmt.Println()
		fmt.Printf("Mrpack file: %d mods (Modrinth only)\n", len(mrpackMods))
		fmt.Println()
		fmt.Printf("Modrinth mod comparison:\n")
		fmt.Printf("- Missing from current pack: %d\n", len(missing))
		fmt.Printf("- Extra in current pack: %d\n", len(extra))
		fmt.Printf("- Version differences: %d\n", len(different))
		fmt.Printf("- Total differences: %d\n", totalDiffs)

		if totalDiffs == 0 {
			fmt.Println("\nModrinth mods are identical!")
		}

		if otherSourceMods.CurseForge > 0 || otherSourceMods.URL > 0 || otherSourceMods.Other > 0 {
			fmt.Printf("\nℹ️  Note: %d non-Modrinth mods in your pack are not compared\n",
				otherSourceMods.CurseForge+otherSourceMods.URL+otherSourceMods.Other)
		}
	},
}

// ModInfo represents information about a mod
type ModInfo struct {
	ProjectID   string
	VersionID   string
	ProjectName string
	FileName    string
	Side        string
}

// ModSources represents mod counts by source
type ModSources struct {
	Modrinth   int
	CurseForge int
	URL        int
	Other      int
}

// hashVersionInfo holds hash and version info for batch processing
type hashVersionInfo struct {
	hash        string
	versionInfo HashResponse
}

// parseMrpackFile extracts the modrinth.index.json from an mrpack file
func parseMrpackFile(mrpackPath string) (*ModrinthIndexFile, error) {
	r, err := openMrpackFile(mrpackPath)
	if err != nil {
		return nil, err
	}
	defer r.Close()

	return extractModrinthIndex(r)
}

// getCurrentModrinthMods gets all Modrinth mods from the current pack and counts other sources
func getCurrentModrinthMods(index *core.Index, progressContainer *mpb.Progress) (map[string]ModInfo, ModSources, error) {
	mods := make(map[string]ModInfo)
	sources := ModSources{}

	// Count total mod files for progress bar
	totalMods := 0
	for _, fileData := range index.Files {
		if fileData.IsMetaFile() {
			totalMods++
		}
	}

	// Create progress bar for analyzing current mods
	var progressBar *mpb.Bar
	if totalMods > 0 {
		progressBar = progressContainer.AddBar(int64(totalMods),
			mpb.PrependDecorators(
				decor.Name("Analyzing mods: "),
				decor.CountersNoUnit("%d / %d"),
			),
			mpb.AppendDecorators(
				decor.Percentage(),
			),
		)
	}

	// Get all mod paths and load them individually to handle missing files gracefully
	processedMods := 0
	for fileName, fileData := range index.Files {
		if fileData.IsMetaFile() {
			modPath := index.ResolveIndexPath(fileName)
			mod, err := core.LoadMod(modPath)
			if err != nil {
				// Skip invalid mod files with warning
				fmt.Printf("Warning: Skipping invalid mod file %s: %v\n", fileName, err)
				if progressBar != nil {
					progressBar.Increment()
					time.Sleep(5 * time.Millisecond) // Small delay to make progress visible
				}
				processedMods++
				continue
			}

			// Check mod source and count
			hasModrinth := false

			// Check if this is a Modrinth mod
			if data, ok := mod.GetParsedUpdateData("modrinth"); ok {
				if updateData, ok := data.(mrUpdateData); ok && updateData.ProjectID != "" {
					mods[updateData.ProjectID] = ModInfo{
						ProjectID:   updateData.ProjectID,
						VersionID:   updateData.InstalledVersion,
						ProjectName: mod.Name,
						FileName:    mod.FileName,
						Side:        mod.Side,
					}
					sources.Modrinth++
					hasModrinth = true
				}
			}

			// If not Modrinth, check other sources
			if !hasModrinth {
				// Check for CurseForge
				if _, ok := mod.GetParsedUpdateData("curseforge"); ok {
					sources.CurseForge++
				} else {
					// Check if it's a direct URL or other source
					if mod.Download.URL != "" {
						// Simple heuristic: if it has update data but not modrinth/curseforge, it's "other"
						// If no update data, it's likely a direct URL
						hasOtherUpdate := false
						for key := range mod.Update {
							if key != "modrinth" && key != "curseforge" {
								hasOtherUpdate = true
								break
							}
						}

						if hasOtherUpdate {
							sources.Other++
						} else {
							sources.URL++
						}
					}
				}
			}

			// Update progress
			if progressBar != nil {
				progressBar.Increment()
				time.Sleep(5 * time.Millisecond) // Small delay to make progress visible
			}
			processedMods++
		}
	}

	// Complete the progress bar
	if progressBar != nil {
		progressBar.SetTotal(int64(processedMods), true)
	}

	return mods, sources, nil
}

// getMrpackModsWithInfo gets mod info from mrpack with API lookups
func getMrpackModsWithInfo(mrpackData *ModrinthIndexFile, progressContainer *mpb.Progress) (map[string]ModInfo, error) {
	mods := make(map[string]ModInfo)

	// Get hashes for API lookup
	var hashes []string
	hashToFile := make(map[string]ModrinthIndexFileRef)

	for _, file := range mrpackData.Files {
		if sha512, ok := file.Hashes["sha512"]; ok {
			hashes = append(hashes, sha512)
			hashToFile[sha512] = file
		}
	}

	if len(hashes) == 0 {
		return mods, nil
	}

	// Look up version IDs from hashes
	versionMap, err := lookupVersionsByHash(hashes)
	if err != nil {
		return nil, fmt.Errorf("failed to lookup versions by hash: %v", err)
	}

	// Collect all unique project IDs for batch API call
	var projectIDs []string
	projectToVersionMap := make(map[string][]hashVersionInfo) // Map project ID to all its versions/hashes

	for hash, versionInfo := range versionMap {
		if projectToVersionMap[versionInfo.ProjectID] == nil {
			projectIDs = append(projectIDs, versionInfo.ProjectID)
		}
		projectToVersionMap[versionInfo.ProjectID] = append(projectToVersionMap[versionInfo.ProjectID], hashVersionInfo{
			hash:        hash,
			versionInfo: versionInfo,
		})
	}

	// Create progress bar for batch API lookup
	var progressBar *mpb.Bar
	if len(projectIDs) > 0 {
		progressBar = progressContainer.AddBar(1,
			mpb.PrependDecorators(
				decor.Name("Fetching project data (batch): "),
				decor.CountersNoUnit("%d / %d"),
			),
			mpb.AppendDecorators(
				decor.Percentage(),
			),
		)
	}

	// Make single batch API call to get all project information
	// This is much more efficient than making individual API calls for each project
	if progressBar != nil {
		progressBar.SetTotal(1, false) // Single batch call
	}

	projects, err := mrDefaultClient.Projects.GetMultiple(projectIDs)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch project information in batch: %v", err)
	}

	if progressBar != nil {
		progressBar.Increment()
		progressBar.SetTotal(1, true)
	}

	// Process each project and create ModInfo entries
	for _, project := range projects {
		if project.ID == nil {
			continue
		}

		projectID := *project.ID
		versionInfos := projectToVersionMap[projectID]

		// Determine side from API data
		side := getSide(project)
		if side == "" {
			side = core.UniversalSide
		}

		// Create ModInfo for each version of this project
		for _, info := range versionInfos {
			fileRef := hashToFile[info.hash]

			mods[projectID] = ModInfo{
				ProjectID:   projectID,
				VersionID:   info.versionInfo.ID,
				ProjectName: *project.Title,
				FileName:    fileRef.Path,
				Side:        side,
			}
		}
	}

	return mods, nil
}

// compareMods compares current mods with mrpack mods
func compareMods(current, mrpack map[string]ModInfo) (missing, extra, different []ModInfo) {
	// Find missing mods (in mrpack but not in current)
	for projectID, mod := range mrpack {
		if _, exists := current[projectID]; !exists {
			missing = append(missing, mod)
		}
	}

	// Find extra mods (in current but not in mrpack)
	for projectID, mod := range current {
		if _, exists := mrpack[projectID]; !exists {
			extra = append(extra, mod)
		}
	}

	// Find version differences (in both but different versions)
	for projectID, currentMod := range current {
		if mrpackMod, exists := mrpack[projectID]; exists {
			if currentMod.VersionID != mrpackMod.VersionID {
				// Create a combined info for display
				diffMod := ModInfo{
					ProjectID:   projectID,
					ProjectName: currentMod.ProjectName,
					VersionID:   fmt.Sprintf("%s → %s", currentMod.VersionID, mrpackMod.VersionID),
					Side:        currentMod.Side,
				}
				different = append(different, diffMod)
			}
		}
	}

	// Sort for consistent output
	slices.SortFunc(missing, func(a, b ModInfo) int {
		return strings.Compare(a.ProjectName, b.ProjectName)
	})
	slices.SortFunc(extra, func(a, b ModInfo) int {
		return strings.Compare(a.ProjectName, b.ProjectName)
	})
	slices.SortFunc(different, func(a, b ModInfo) int {
		return strings.Compare(a.ProjectName, b.ProjectName)
	})

	return missing, extra, different
}

// displayDifferences shows the comparison results
func displayDifferences(missing, extra, different []ModInfo) {
	if len(missing) > 0 {
		fmt.Printf("- Missing from current pack (%d):\n", len(missing))
		for _, mod := range missing {
			fmt.Printf("  + %s [%s] (side: %s)\n", mod.ProjectName, mod.ProjectID, mod.Side)
		}
		fmt.Println()
	}

	if len(extra) > 0 {
		fmt.Printf("- Extra in current pack (%d):\n", len(extra))
		for _, mod := range extra {
			fmt.Printf("  - %s [%s] (side: %s)\n", mod.ProjectName, mod.ProjectID, mod.Side)
		}
		fmt.Println()
	}

	if len(different) > 0 {
		fmt.Printf("- Version differences (%d):\n", len(different))
		for _, mod := range different {
			fmt.Printf("  ~ %s [%s] (side: %s)\n", mod.ProjectName, mod.VersionID, mod.Side)
		}
		fmt.Println()
	}

	if len(missing) == 0 && len(extra) == 0 && len(different) == 0 {
		fmt.Println("No differences found!")
	}
}

// openMrpackFile opens an mrpack file for reading
func openMrpackFile(mrpackPath string) (*zip.ReadCloser, error) {
	return zip.OpenReader(mrpackPath)
}

func init() {
	modrinthCmd.AddCommand(diffCmd)
}
