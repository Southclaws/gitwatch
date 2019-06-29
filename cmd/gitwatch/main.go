package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/Southclaws/gitwatch"
	"github.com/pkg/errors"
	"github.com/urfave/cli"
	"golang.org/x/xerrors"
	"gopkg.in/src-d/go-git.v4/plumbing/transport/ssh"
)

func main() {
	app := cli.NewApp()
	app.Name = "gitwatch"
	app.Usage = "Writes to stdout whenever the specified git repository receives a commit"
	app.UsageText = "gitwatch [flags] [repositories with ssh URLs]"
	app.HideVersion = true
	app.HideHelp = true

	app.Flags = []cli.Flag{
		cli.DurationFlag{
			Name:   "interval",
			EnvVar: "GITWATCH_INTERVAL",
			Value:  time.Millisecond * 100,
		},
		cli.StringFlag{
			Name:   "dir",
			EnvVar: "GITWATCH_DIRECTORY",
			Value:  "gitwatch",
		},
		cli.BoolFlag{
			Name:   "initial-event",
			EnvVar: "GITWATCH_INITIAL_EVENT",
		},
	}
	app.Action = func(c *cli.Context) (err error) {
		repos := c.Args()

		if len(repos) == 0 {
			return cli.ShowAppHelp(c)
		}

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		auth, err := ssh.NewSSHAgentAuth("git")
		if err != nil {
			return errors.Wrap(err, "failed to set up SSH authentication")
		}

		interval := c.Duration("interval")
		dir := c.String("dir")
		initialEvent := c.Bool("initial-event")

		fmt.Printf("interval: %v, dir: %v, initial event: %v\n", interval, dir, initialEvent)

		watch, err := gitwatch.New(
			ctx,
			repos,
			interval,
			dir,
			auth,
			initialEvent,
		)
		if err != nil {
			return errors.Wrap(err, "failed to initialise watcher")
		}

		go func() {
			for {
				select {
				case e := <-watch.Events:
					fmt.Println("Event:", e)
				case e := <-watch.Errors:
					if xerrors.Is(e, io.EOF) {
						fmt.Println("EOF:", e)
					}
					fmt.Println("Error:", e)
				}
			}
		}()

		return watch.Run()
	}
	if err := app.Run(os.Args); err != nil {
		fmt.Println(err)
	}
}
