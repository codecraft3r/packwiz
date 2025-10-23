package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/codecraft3r/packwiz/core"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

// validateCmd represents the validate command
var validateCmd = &cobra.Command{
	Use:   "validate",
	Short: "Validate pack integrity and file formats",
	Long: `Validate checks the integrity of your pack by:
- Verifying all mod metadata files are properly formatted
- Checking that all files referenced in the index exist
- Ensuring the index is consistent with actual files
- Validating pack.toml format
- Reporting any issues found`,
	Run: func(cmd *cobra.Command, args []string) {
		// Load pack
		pack, err := core.LoadPack()
		if err != nil {
			fmt.Printf("Failed to load pack: %v\n", err)
			os.Exit(1)
		}

		fmt.Printf("Validating pack: %s\n", pack.Name)
		if pack.Description != "" {
			fmt.Printf("Description: %s\n", pack.Description)
		}
		fmt.Println()

		// Load index
		index, err := pack.LoadIndex()
		if err != nil {
			fmt.Printf("Failed to load index: %v\n", err)
			os.Exit(1)
		}

		// Run validation checks
		issues := 0
		warnings := 0

		// 1. Validate pack.toml format
		fmt.Println("✓ Checking pack.toml format...")
		if pack.Name == "" {
			fmt.Println("  ERROR: Pack name is empty")
			issues++
		}

		// Check MC version
		mcVersion, err := pack.GetMCVersion()
		if err != nil {
			fmt.Printf("     WARNING: Could not determine MC version: %v\n", err)
			warnings++
		} else if mcVersion == "" {
			fmt.Println("     WARNING: MC version is empty")
			warnings++
		}

		if len(pack.Versions) == 0 {
			fmt.Println("     WARNING: No supported MC versions specified")
			warnings++
		}

		// 2. Validate index.toml format
		fmt.Println("✓ Checking index.toml format...")
		if len(index.Files) == 0 {
			fmt.Println("     WARNING: Index contains no files")
			warnings++
		}

		// 3. Check for orphaned files in index (files that don't exist on disk)
		fmt.Println("✓ Checking for missing files referenced in index...")
		orphanedFiles := 0
		for fileName := range index.Files {
			filePath := index.ResolveIndexPath(fileName)
			if _, err := os.Stat(filePath); os.IsNotExist(err) {
				fmt.Printf("  ERROR: File referenced in index but missing: %s\n", fileName)
				issues++
				orphanedFiles++
			}
		}
		if orphanedFiles == 0 {
			fmt.Println("  All indexed files exist")
		}

		// 4. Validate mod metadata files
		fmt.Println("✓ Checking mod metadata file formats...")
		validMods := 0
		invalidMods := 0

		for fileName, fileData := range index.Files {
			if fileData.IsMetaFile() {
				filePath := index.ResolveIndexPath(fileName)

				// Try to load the mod file
				mod, err := core.LoadMod(filePath)
				if err != nil {
					fmt.Printf("  ERROR: Invalid mod file %s: %v\n", fileName, err)
					issues++
					invalidMods++
					continue
				}

				// Validate mod structure
				if mod.Name == "" {
					fmt.Printf("  ERROR: Mod file %s has empty name\n", fileName)
					issues++
					invalidMods++
					continue
				}

				if mod.FileName == "" {
					fmt.Printf("  ERROR: Mod file %s has empty filename\n", fileName)
					issues++
					invalidMods++
					continue
				}

				if mod.Download.URL == "" {
					fmt.Printf("  ERROR: Mod file %s has empty download URL\n", fileName)
					issues++
					invalidMods++
					continue
				}

				if mod.Download.HashFormat == "" || mod.Download.Hash == "" {
					fmt.Printf("  ERROR: Mod file %s has missing hash information\n", fileName)
					issues++
					invalidMods++
					continue
				}

				// Validate side field
				if mod.Side != "" {
					err := core.ValidateSide(mod.Side)
					if err != nil {
						fmt.Printf("  ERROR: Mod file %s has invalid side '%s': %v\n", fileName, mod.Side, err)
						issues++
						invalidMods++
						continue
					}
				}

				validMods++
			}
		}

		if invalidMods == 0 {
			fmt.Printf("  All %d mod files are valid\n", validMods)
		} else {
			fmt.Printf("  %d mod files are invalid, %d are valid\n", invalidMods, validMods)
		}

		// 5. Check for untracked mod files (mod files that exist but aren't in index)
		fmt.Println("✓ Checking for untracked mod files...")
		untrackedFiles := 0

		// Walk through common mod directories
		modDirs := []string{"mods", "resourcepacks", "shaderpacks", "datapacks", "plugins"}
		for _, dir := range modDirs {
			dirPath := index.ResolveIndexPath(dir)
			if _, err := os.Stat(dirPath); os.IsNotExist(err) {
				continue // Directory doesn't exist, skip
			}

			err := filepath.Walk(dirPath, func(path string, info os.FileInfo, err error) error {
				if err != nil {
					return err
				}

				if !info.IsDir() && strings.HasSuffix(path, core.MetaExtension) {
					// Convert to relative path for index lookup
					// Get pack root by looking up from any indexed file
					packRoot := ""
					for fileName := range index.Files {
						fullPath := index.ResolveIndexPath(fileName)
						if dir := filepath.Dir(fullPath); dir != "" {
							packRoot = strings.TrimSuffix(fullPath, fileName)
							break
						}
					}
					if packRoot == "" {
						packRoot = filepath.Dir(index.ResolveIndexPath(""))
					}

					relPath, err := filepath.Rel(packRoot, path)
					if err != nil {
						return err
					}
					relPath = filepath.ToSlash(relPath) // Normalize to forward slashes

					// Check if this file is in the index
					if _, exists := index.Files[relPath]; !exists {
						fmt.Printf("     WARNING: Untracked mod file: %s\n", relPath)
						warnings++
						untrackedFiles++
					}
				}
				return nil
			})

			if err != nil {
				fmt.Printf("     WARNING: Error scanning directory %s: %v\n", dir, err)
				warnings++
			}
		}

		if untrackedFiles == 0 {
			fmt.Println("  No untracked mod files found")
		}

		// 6. Validate index hash consistency
		fmt.Println("✓ Checking index hash consistency...")
		if pack.Index.Hash == "" {
			fmt.Println("     WARNING: No index hash specified in pack.toml")
			warnings++
		} else {
			// Calculate current index hash
			currentHash, err := calculateIndexHash(pack)
			if err != nil {
				fmt.Printf("  ERROR: Failed to calculate current index hash: %v\n", err)
				issues++
			} else {
				if pack.Index.Hash != currentHash {
					fmt.Printf("  ERROR: Index hash mismatch - pack.toml shows %s but calculated %s\n",
						pack.Index.Hash, currentHash)
					fmt.Println("         Run 'packwiz refresh' to fix this")
					issues++
				} else {
					fmt.Println("  Index hash is consistent")
				}
			}
		}

		// Summary
		fmt.Println()
		fmt.Println("=== Validation Summary ===")

		if issues == 0 && warnings == 0 {
			fmt.Println("Pack validation passed with no issues!")
		} else if issues == 0 {
			fmt.Printf("Pack validation passed with %d warning(s)\n", warnings)
		} else {
			fmt.Printf("Pack validation failed with %d error(s) and %d warning(s)\n", issues, warnings)
		}

		fmt.Printf("Files checked: %d total, %d mod files, %d other files\n",
			len(index.Files), validMods+invalidMods, len(index.Files)-(validMods+invalidMods))

		if issues > 0 {
			fmt.Println("\nRecommended actions:")
			fmt.Println("- Fix any ERROR items listed above")
			fmt.Println("- Run 'packwiz refresh' to update the index")
			fmt.Println("- Remove any orphaned references from index.toml")
			os.Exit(1)
		} else if warnings > 0 {
			fmt.Println("\nConsider addressing WARNING items for better pack quality")
		}
	},
}

// calculateIndexHash calculates the hash of the index file
func calculateIndexHash(pack core.Pack) (string, error) {
	packFilePath := "pack.toml" // Default
	if viper.IsSet("pack-file") {
		packFilePath = viper.GetString("pack-file")
	}

	indexPath := filepath.Join(filepath.Dir(packFilePath), filepath.FromSlash(pack.Index.File))

	// Read file content
	content, err := os.ReadFile(indexPath)
	if err != nil {
		return "", err
	}

	// Use SHA256 as default (same as pack.Index.HashFormat or fallback)
	hashFormat := pack.Index.HashFormat
	if hashFormat == "" {
		hashFormat = "sha256"
	}

	hasher, err := core.GetHashImpl(hashFormat)
	if err != nil {
		return "", err
	}

	// Hash the content
	_, err = hasher.Write(content)
	if err != nil {
		return "", err
	}

	// Get the final hash bytes and format as string
	hashBytes := hasher.Sum(nil)
	return hasher.HashToString(hashBytes), nil
}

func init() {
	rootCmd.AddCommand(validateCmd)
}
