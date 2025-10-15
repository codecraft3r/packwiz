package main

import (
	// Modules of packwiz
	"github.com/codecraft3r/packwiz/cmd"
	_ "github.com/codecraft3r/packwiz/curseforge"
	_ "github.com/codecraft3r/packwiz/github"
	_ "github.com/codecraft3r/packwiz/migrate"
	_ "github.com/codecraft3r/packwiz/modrinth"
	_ "github.com/codecraft3r/packwiz/settings"
	_ "github.com/codecraft3r/packwiz/url"
	_ "github.com/codecraft3r/packwiz/utils"
)

func main() {
	cmd.Execute()
}
