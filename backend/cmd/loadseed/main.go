// Command loadseed creates the reference dataset used by deploy/loadtest. It is
// a development tool: it prints the load API key once and never runs in the
// production stack.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"o1/internal/app"
	"o1/internal/loadseed"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run() error {
	var opts loadseed.Options
	flag.IntVar(&opts.Tenants, "tenants", 10, "number of workspaces")
	flag.IntVar(&opts.MonitorsPerTenant, "monitors", 20, "monitors per workspace")
	flag.IntVar(&opts.RawDays, "raw-days", 7, "days of minute-level probe results")
	flag.IntVar(&opts.RollupDays, "rollup-days", 90, "days of hourly rollups")
	flag.StringVar(&opts.Password, "password", loadseed.DefaultPassword, "password for the load account")
	flag.Parse()

	ctx := context.Background()
	rt, err := app.Start(ctx)
	if err != nil {
		return err
	}
	defer rt.Close()

	res, err := loadseed.Seed(ctx, rt.Pool, opts)
	if err != nil {
		return err
	}

	fmt.Printf("load dataset ready\n")
	fmt.Printf("  tenants      %d\n", len(res.WorkspaceIDs))
	fmt.Printf("  monitors     %d\n", res.Monitors)
	fmt.Printf("  raw rows     %d\n", res.RawRows)
	fmt.Printf("  rollup rows  %d\n", res.RollupRows)
	fmt.Printf("  account      %s (password: %s)\n", loadseed.LoadEmail, opts.Password)
	fmt.Printf("  api key      %s\n", res.APIKey)
	fmt.Printf("  the api key is shown once; export it as API_KEY for k6\n")
	return nil
}
