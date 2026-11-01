package cli

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/chaseungjoon/recom/internal/buildinfo"
	"github.com/chaseungjoon/recom/internal/selfupdate"
)

func updateCmd(gf *globalFlags) *cobra.Command {
	var check, force bool
	var version string
	cmd := &cobra.Command{
		Use:   "update",
		Short: "Update recom to the latest release",
		Long: `Download the latest recom release from GitHub, verify it against the
release's SHA-256 checksums, and atomically replace this binary. Backends
pick up the matching helper automatically on the next connection.

  recom update                  install the latest release if it is newer
  recom update --check          only report whether an update is available
  recom update --version v0.2.0 install a specific release (also downgrades)

If recom lives in a directory you cannot write to (e.g. /usr/local/bin),
run ` + "`sudo recom update`" + `.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			a := newApp(cmd, gf)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
			defer cancel()
			u := selfupdate.New()
			exe, err := selfupdate.Executable()
			if err != nil {
				return fmt.Errorf("cannot locate the recom binary: %w", err)
			}
			if pm := selfupdate.ManagedBy(exe); pm != "" && !check {
				return fmt.Errorf("%s was installed with %s, which no longer distributes recom; remove it (brew uninstall recom) and reinstall with:\n  %s", exe, pm, selfupdate.InstallScript)
			}
			cur, curOK := selfupdate.Parse(buildinfo.Version)
			tag := version
			if tag == "" {
				if tag, err = u.Latest(ctx); err != nil {
					return err
				}
			} else if !strings.HasPrefix(tag, "v") {
				tag = "v" + tag
			}
			want, ok := selfupdate.Parse(tag)
			if !ok {
				return exitWith(ExitUsage, fmt.Errorf("invalid version %q (expected e.g. v0.2.0)", version))
			}
			current := buildinfo.Version
			switch {
			case !curOK:
				a.info("this is a development build (%s); the latest release is %s", current, tag)
				if check {
					return nil
				}
				if !force && version == "" {
					return errors.New("not replacing a development build; pass --force to install the release anyway")
				}
			case version == "" && !cur.Less(want) && !force:
				fmt.Fprintf(a.out, "recom %s is up to date (latest release: %s)\n", cur, tag)
				return nil
			case check:
				fmt.Fprintf(a.out, "update available: recom %s → %s (run `recom update`)\n", cur, want)
				return nil
			}
			if check {
				return nil
			}
			a.info("downloading %s", u.Archive(tag))
			bin, err := u.Fetch(ctx, tag)
			if err != nil {
				return err
			}
			a.info("checksum verified")
			if err := selfupdate.Install(exe, bin, want.String()); err != nil {
				if errors.Is(err, selfupdate.ErrPermission) {
					return fmt.Errorf("%v; run `sudo recom update`, or reinstall into a directory you own:\n  %s", err, strings.Replace(selfupdate.InstallScript, "| sh", "| RECOM_INSTALL_DIR=\"$HOME/.local/bin\" sh", 1))
				}
				return err
			}
			from := current
			if curOK {
				from = cur.String()
			}
			fmt.Fprintf(a.out, "updated %s: recom %s → %s\n", exe, from, want)
			a.info("backends get the matching helper automatically on the next connection")
			return nil
		},
	}
	cmd.Flags().BoolVar(&check, "check", false, "only check whether a newer release exists")
	cmd.Flags().StringVar(&version, "version", "", "install this release tag instead of the latest (e.g. v0.2.0)")
	cmd.Flags().BoolVar(&force, "force", false, "reinstall even if already up to date (or replace a development build)")
	return cmd
}
