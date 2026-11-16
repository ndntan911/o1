// Command seed creates or refreshes the demo workspace: a verified account,
// public monitors, synthetic 90-day history, incidents, and a published status
// page. It is safe to run repeatedly and prints the generated password once.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"o1/internal/app"
	"o1/internal/demo"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run() error {
	var opts demo.Options
	flag.StringVar(&opts.Email, "email", demo.DefaultEmail, "demo account email")
	flag.StringVar(&opts.Password, "password", "", "demo account password (random when omitted and the account is new)")
	flag.StringVar(&opts.WorkspaceName, "workspace", demo.DefaultWorkspaceName, "demo workspace name")
	flag.StringVar(&opts.Slug, "slug", demo.DefaultSlug, "workspace slug, also the status page path")
	flag.Parse()

	ctx := context.Background()
	rt, err := app.Start(ctx)
	if err != nil {
		return err
	}
	defer rt.Close()

	res, err := demo.Seed(ctx, rt.Pool, opts)
	if err != nil {
		return err
	}

	fmt.Printf("demo workspace ready\n")
	fmt.Printf("  status page  %s\n", res.StatusPath)
	fmt.Printf("  workspace    %s\n", res.WorkspaceID)
	if res.AccountCreated {
		fmt.Printf("  account      %s (created)\n", opts.Email)
		if res.Password != "" {
			fmt.Printf("  password     %s\n", res.Password)
			fmt.Printf("  the password is shown once; pass -password to choose one\n")
		} else {
			fmt.Printf("  password     as provided\n")
		}
	} else {
		fmt.Printf("  account      %s (existing)\n", opts.Email)
		fmt.Printf("  password     unchanged; pass -password to reset it\n")
	}
	fmt.Printf("  monitors     %d\n", res.Monitors)
	fmt.Printf("  history      %d hourly buckets\n", res.HourlyRows)
	fmt.Printf("  today        %d probe results\n", res.RawRows)
	fmt.Printf("  incidents    %d\n", res.Incidents)
	return nil
}
