package cmd

import (
	"fmt"
	"os"
	"strings"

	"github.com/packwiz/packwiz/core"
	"github.com/spf13/cobra"
)

// modifyCmd represents the modify command
var modifyCmd = &cobra.Command{
	Use:   "modify [mod name/path]",
	Short: "Modify properties of an existing mod",
	Long: `Modify properties of an existing mod such as side compatibility, 
disabled client platforms, pin status, and optional settings.

Examples:
  packwiz modify jei --side client
  packwiz modify optifine --disabled-client-platforms macos,linux
  packwiz modify sodium --pin
  packwiz modify rei --optional --optional-description "Enhanced recipe viewing"`,
	Args: cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		modifyModProperties(cmd, args)
	},
}

func modifyModProperties(cmd *cobra.Command, args []string) {
	fmt.Println("Loading modpack...")
	pack, err := core.LoadPack()
	if err != nil {
		fmt.Printf("Failed to load pack: %v\n", err)
		os.Exit(1)
	}

	index, err := pack.LoadIndex()
	if err != nil {
		fmt.Printf("Failed to load index: %v\n", err)
		os.Exit(1)
	}

	// Find the mod
	modPath, ok := index.FindMod(args[0])
	if !ok {
		fmt.Printf("Cannot find mod '%s'. Please ensure you have run 'packwiz refresh' and use the correct mod name/slug.\n", args[0])
		os.Exit(1)
	}

	// Load the mod
	modData, err := core.LoadMod(modPath)
	if err != nil {
		fmt.Printf("Failed to load mod: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Modifying mod: %s\n", modData.Name)

	// Track if any changes were made
	changed := false

	// Handle side modification
	if cmd.Flags().Changed("side") {
		side, _ := cmd.Flags().GetString("side")
		side = strings.TrimSpace(side) // Clean up input
		
		if err := core.ValidateSide(side); err != nil {
			fmt.Printf("Side validation error: %v\n", err)
			os.Exit(1)
		}
		
		// Normalize the side value
		normalizedSide := core.NormalizeSide(side)
		
		oldSide := modData.Side
		if oldSide == "" {
			oldSide = "both" // default side display
		}
		
		modData.Side = normalizedSide
		
		// Show user-friendly names in output
		displaySide := normalizedSide
		if displaySide == core.UniversalSide {
			displaySide = "both"
		}
		
		fmt.Printf("Changed side from '%s' to '%s'\n", oldSide, displaySide)
		changed = true
	}

	// Handle disabled client platforms
	if cmd.Flags().Changed("disabled-client-platforms") {
		platforms, _ := cmd.Flags().GetStringSlice("disabled-client-platforms")
		
		// Validate platforms using core validation
		if err := core.ValidateClientPlatforms(platforms); err != nil {
			fmt.Printf("Platform validation error: %v\n", err)
			os.Exit(1)
		}
		
		// Normalize and deduplicate platforms
		normalizedPlatforms := core.NormalizeClientPlatforms(platforms)
		
		oldPlatforms := modData.Download.DisabledClientPlatforms
		modData.Download.DisabledClientPlatforms = normalizedPlatforms
		
		if len(normalizedPlatforms) == 0 {
			fmt.Printf("Cleared disabled client platforms (was %v)\n", oldPlatforms)
		} else {
			fmt.Printf("Changed disabled client platforms from %v to %v\n", oldPlatforms, normalizedPlatforms)
		}
		changed = true
	}

	// Handle pin status
	if cmd.Flags().Changed("pin") {
		pin, _ := cmd.Flags().GetBool("pin")
		oldPin := modData.Pin
		modData.Pin = pin
		if pin {
			fmt.Printf("Pinned mod (was %t)\n", oldPin)
		} else {
			fmt.Printf("Unpinned mod (was %t)\n", oldPin)
		}
		changed = true
	}

	// Handle optional settings
	optionalChanged := false
	if cmd.Flags().Changed("optional") || cmd.Flags().Changed("optional-description") || cmd.Flags().Changed("optional-default") {
		// Ensure ModOption exists if we're modifying optional settings
		if modData.Option == nil {
			modData.Option = &core.ModOption{}
		}

		if cmd.Flags().Changed("optional") {
			optional, _ := cmd.Flags().GetBool("optional")
			oldOptional := modData.Option.Optional
			modData.Option.Optional = optional
			fmt.Printf("Changed optional status from %t to %t\n", oldOptional, optional)
			optionalChanged = true
		}

		if cmd.Flags().Changed("optional-description") {
			description, _ := cmd.Flags().GetString("optional-description")
			oldDescription := modData.Option.Description
			modData.Option.Description = description
			fmt.Printf("Changed optional description from '%s' to '%s'\n", oldDescription, description)
			optionalChanged = true
		}

		if cmd.Flags().Changed("optional-default") {
			defaultVal, _ := cmd.Flags().GetBool("optional-default")
			oldDefault := modData.Option.Default
			modData.Option.Default = defaultVal
			fmt.Printf("Changed optional default from %t to %t\n", oldDefault, defaultVal)
			optionalChanged = true
		}

		// If all optional settings are default values, remove the Option struct
		if modData.Option != nil && !modData.Option.Optional && modData.Option.Description == "" && !modData.Option.Default {
			modData.Option = nil
			fmt.Println("Removed optional settings (all values were default)")
		}

		if optionalChanged {
			changed = true
		}
	}

	// Check if any changes were made
	if !changed {
		fmt.Println("No changes specified. Use --help to see available options.")
		return
	}

	// Save the modified mod
	format, hash, err := modData.Write()
	if err != nil {
		fmt.Printf("Failed to write mod file: %v\n", err)
		os.Exit(1)
	}

	// Update the index
	err = index.RefreshFileWithHash(modPath, format, hash, true)
	if err != nil {
		fmt.Printf("Failed to refresh index: %v\n", err)
		os.Exit(1)
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

	fmt.Printf("Successfully modified mod '%s'\n", modData.Name)
}

// Note: Validation functions are now in core package for reuse across commands

func init() {
	rootCmd.AddCommand(modifyCmd)

	// Add flags for various modification options
	modifyCmd.Flags().String("side", "", "Set the mod side (client, server, both)")
	modifyCmd.Flags().StringSlice("disabled-client-platforms", []string{}, "Set disabled client platforms (macos, linux, windows)")
	modifyCmd.Flags().Bool("pin", false, "Pin or unpin the mod (use --pin=true to pin, --pin=false to unpin)")
	modifyCmd.Flags().Bool("optional", false, "Mark the mod as optional (use --optional=true for optional, --optional=false for required)")
	modifyCmd.Flags().String("optional-description", "", "Set the description for the optional mod")
	modifyCmd.Flags().Bool("optional-default", false, "Set whether the optional mod is enabled by default (use --optional-default=true or --optional-default=false)")

	// Add some examples to the help
	modifyCmd.Example = `  # Change a mod to client-side only
  packwiz modify jei --side client

  # Disable a mod on macOS and Linux
  packwiz modify optifine --disabled-client-platforms macos,linux

  # Pin a mod to prevent updates
  packwiz modify sodium --pin=true

  # Make a mod optional with a description
  packwiz modify rei --optional=true --optional-description "Enhanced recipe viewing"

  # Clear disabled platforms (enable on all platforms)
  packwiz modify mymod --disabled-client-platforms ""

  # Unpin a mod
  packwiz modify sodium --pin=false`
}