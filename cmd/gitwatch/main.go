package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/Southclaws/gitwatch/v2"
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
			MakeRepositoryList(repos),
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

// MakeRepositoryList Creates a repository list from an array of
// strings, while also checking is the string contains a special
// character which can be used to get the branch to use
func MakeRepositoryList(repos []string) []gitwatch.Repository {
	result := make([]gitwatch.Repository, len(repos))
	for i, repo := range repos {
		url := repo
		branch := "master"

		if strings.Contains(repo, "#") {
			path := strings.Split(repo, "#")

			url = path[0]
			if len(path[1]) > 0 {
				branch = path[1]
			}
		}

		result[i] = gitwatch.Repository{
			URL:    url,
			Branch: branch,
		}
	}
	return result
}
