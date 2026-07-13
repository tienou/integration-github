package main

import (
	"os"

	sdk "github.com/PlakarKorp/go-kloset-sdk"
	"github.com/tienou/integration-github/github"
)

func main() {
	sdk.EntrypointImporter(os.Args, github.NewGithubImporter)
}
