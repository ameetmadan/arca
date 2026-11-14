// Command decisionbench compares the local decision chain with the same chain
// plus the optional hosted engine (ADR-0008) on a labelled set of real files.
//
// WARNING: the hosted run sends a short snippet of every labelled file in an
// opted-in folder to the hosted service, off this Mac. Use a scratch copy or
// files you are happy to send. It needs the hosted engine configured (see the
// README) and its API key in the macOS Keychain.
//
//	decisionbench -config ~/Library/Application\ Support/Arca/config.yaml -labels labels.csv
//
// labels.csv has one `absolute/path,folder/relative/to/watched/root` per line.
// Output is a Markdown table of aggregate scores, ready to paste into an ADR.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"

	"github.com/ameetmadan/arca/internal/config"
	"github.com/ameetmadan/arca/internal/decisionbench"
	"github.com/ameetmadan/arca/internal/pipeline"
)

func main() {
	cfgPath := flag.String("config", "", "path to config.yaml (required)")
	labels := flag.String("labels", "", "path to the labels CSV (required)")
	flag.Parse()
	if *cfgPath == "" || *labels == "" {
		flag.Usage()
		os.Exit(2)
	}
	if err := run(*cfgPath, *labels); err != nil {
		log.Fatalf("decisionbench: %v", err)
	}
}

func run(cfgPath, labelsPath string) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	lf, err := os.Open(labelsPath)
	if err != nil {
		return err
	}
	defer lf.Close()
	cases, err := decisionbench.ReadLabels(lf)
	if err != nil {
		return err
	}

	app, err := config.Load(cfgPath)
	if err != nil {
		return err
	}
	defer app.Close()
	if app.LocalDecision == nil {
		return fmt.Errorf("the config has no hosted engine to compare; add a type: hosted step with opt_in_folders")
	}

	fb := &pipeline.FactsBuilder{Parser: app.Parser, Embed: app.EmbedProvider, Roots: app.Cfg.Watcher.Folders}
	local, err := decisionbench.Run(ctx, "local chain", app.LocalDecision, fb.Build, cases)
	if err != nil {
		return err
	}
	withHosted, err := decisionbench.Run(ctx, "local chain + hosted", app.Decision, fb.Build, cases)
	if err != nil {
		return err
	}
	fmt.Print(decisionbench.Markdown([]decisionbench.Result{local, withHosted}))
	return nil
}
