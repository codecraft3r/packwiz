package core

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/BurntSushi/toml"
)

// Mod stores metadata about a mod. This is written to a TOML file for each mod.
type Mod struct {
	metaFile string      // The file for the metadata file, used as an ID
	Name     string      `toml:"name"`
	FileName string      `toml:"filename"`
	Side     string      `toml:"side,omitempty"`
	Pin      bool        `toml:"pin,omitempty"`
	Download ModDownload `toml:"download"`
	// Update is a map of map of stuff, so you can store arbitrary values on string keys to define updating
	Update     map[string]map[string]interface{} `toml:"update"`
	updateData map[string]interface{}

	Option *ModOption `toml:"option,omitempty"`
}

const (
	ModeURL string = "url"
	ModeCF  string = "metadata:curseforge"
)

// ModDownload specifies how to download the mod file
type ModDownload struct {
	URL                      string   `toml:"url,omitempty"`
	DisabledClientPlatforms  []string `toml:"disabled-client-platforms,omitempty"`
	HashFormat               string   `toml:"hash-format"`
	Hash                     string   `toml:"hash"`
	// Mode defaults to modeURL (i.e. use URL when omitted or empty)
	Mode string `toml:"mode,omitempty"`
}

// ModOption specifies optional metadata for this mod file
type ModOption struct {
	Optional    bool   `toml:"optional"`
	Description string `toml:"description,omitempty"`
	Default     bool   `toml:"default,omitempty"`
}

// The four possible values of Side (the side that the mod is on) are "server", "client", "both", and "" (equivalent to "both")
const (
	ServerSide    = "server"
	ClientSide    = "client"
	UniversalSide = "both"
	EmptySide     = ""
)

// Valid client platforms for DisabledClientPlatforms
var ValidClientPlatforms = []string{"macos", "linux", "windows"}

// LoadMod attempts to load a mod file from a path
func LoadMod(modFile string) (Mod, error) {
	var mod Mod
	if _, err := toml.DecodeFile(modFile, &mod); err != nil {
		return Mod{}, err
	}
	mod.updateData = make(map[string]interface{})
	// Horrible reflection library to convert map[string]interface to proper struct
	for k, v := range mod.Update {
		updater, ok := Updaters[k]
		if ok {
			updateData, err := updater.ParseUpdate(v)
			if err != nil {
				return mod, err
			}
			mod.updateData[k] = updateData
		} else {
			return mod, errors.New("Update plugin " + k + " not found!")
		}
	}
	mod.metaFile = modFile
	return mod, nil
}

// SetMetaPath sets the file path of a metadata file
func (m *Mod) SetMetaPath(metaFile string) string {
	m.metaFile = metaFile
	return m.metaFile
}

// Write saves the mod file, returning a hash format and the value of the hash of the saved file
func (m Mod) Write() (string, string, error) {
	f, err := os.Create(m.metaFile)
	if err != nil {
		// Attempt to create the containing directory
		err2 := os.MkdirAll(filepath.Dir(m.metaFile), os.ModePerm)
		if err2 == nil {
			f, err = os.Create(m.metaFile)
		}
		if err != nil {
			return "sha256", "", err
		}
	}

	h, err := GetHashImpl("sha256")
	if err != nil {
		_ = f.Close()
		return "", "", err
	}
	w := io.MultiWriter(h, f)

	enc := toml.NewEncoder(w)
	// Disable indentation
	enc.Indent = ""
	err = enc.Encode(m)
	hashString := h.HashToString(h.Sum(nil))
	if err != nil {
		_ = f.Close()
		return "sha256", hashString, err
	}
	return "sha256", hashString, f.Close()
}

// GetParsedUpdateData can be used to retrieve updater-specific information after parsing a mod file
func (m Mod) GetParsedUpdateData(updaterName string) (interface{}, bool) {
	upd, ok := m.updateData[updaterName]
	return upd, ok
}

// GetFilePath is a clumsy hack that I made because Mod already stores it's path anyway
func (m Mod) GetFilePath() string {
	return m.metaFile
}

// GetDestFilePath returns the path of the destination file of the mod
func (m Mod) GetDestFilePath() string {
	return filepath.Join(filepath.Dir(m.metaFile), filepath.FromSlash(m.FileName))
}

var slugifyRegex1 = regexp.MustCompile(`\(.*\)`)
var slugifyRegex2 = regexp.MustCompile(` - .+`)
var slugifyRegex3 = regexp.MustCompile(`[^a-z\d]`)
var slugifyRegex4 = regexp.MustCompile(`-+`)
var slugifyRegex5 = regexp.MustCompile(`^-|-$`)

func SlugifyName(name string) string {
	lower := strings.ToLower(name)
	noBrackets := slugifyRegex1.ReplaceAllString(lower, "")
	noSuffix := slugifyRegex2.ReplaceAllString(noBrackets, "")
	limitedChars := slugifyRegex3.ReplaceAllString(noSuffix, "-")
	noDuplicateDashes := slugifyRegex4.ReplaceAllString(limitedChars, "-")
	noLeadingTrailingDashes := slugifyRegex5.ReplaceAllString(noDuplicateDashes, "")
	return noLeadingTrailingDashes
}

// ValidateSide checks if the given side value is valid
func ValidateSide(side string) error {
	if side == "" {
		return errors.New("side cannot be empty")
	}
	
	validSides := []string{ClientSide, ServerSide, UniversalSide, "both"}
	for _, validSide := range validSides {
		if side == validSide {
			return nil
		}
	}
	
	return fmt.Errorf("invalid side '%s'. Valid values are: %s", side, strings.Join(validSides, ", "))
}

// NormalizeSide converts user-friendly side values to internal constants
func NormalizeSide(side string) string {
	side = strings.TrimSpace(side)
	if side == "both" {
		return UniversalSide
	}
	return side
}

// ValidateClientPlatforms checks if all platforms in the slice are valid
func ValidateClientPlatforms(platforms []string) error {
	for _, platform := range platforms {
		// Skip empty strings (allows clearing the list)
		if platform == "" {
			continue
		}
		
		// Trim whitespace and convert to lowercase for more forgiving input
		platform = strings.ToLower(strings.TrimSpace(platform))
		
		found := false
		for _, validPlatform := range ValidClientPlatforms {
			if platform == validPlatform {
				found = true
				break
			}
		}
		
		if !found {
			return fmt.Errorf("invalid platform '%s'. Valid platforms are: %s", 
				platform, strings.Join(ValidClientPlatforms, ", "))
		}
	}
	
	return nil
}

// NormalizeClientPlatforms cleans up and deduplicates platform names
func NormalizeClientPlatforms(platforms []string) []string {
	var normalized []string
	seen := make(map[string]bool)
	
	for _, platform := range platforms {
		// Skip empty strings
		if platform == "" {
			continue
		}
		
		// Trim whitespace and convert to lowercase
		platform = strings.ToLower(strings.TrimSpace(platform))
		
		// Only add if valid and not already present
		if !seen[platform] {
			for _, validPlatform := range ValidClientPlatforms {
				if platform == validPlatform {
					normalized = append(normalized, platform)
					seen[platform] = true
					break
				}
			}
		}
	}
	
	return normalized
}
