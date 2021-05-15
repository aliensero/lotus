package main

import (
	"fmt"
	"os"

	"github.com/filecoin-project/lotus/node"
	"github.com/filecoin-project/lotus/node/repo"
	"github.com/urfave/cli/v2"
)

func main() {
	app := &cli.App{
		Name:  "chain-swap",
		Usage: "chain bitswap in the network",
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:    "repo",
				EnvVars: []string{"CHAIN_PATH"},
				Value:   "chain-swap-repo", // TODO: Consider XDG_DATA_HOME
			},
		},
		Commands: []*cli.Command{runCmd},
	}

	if err := app.Run(os.Args); err != nil {
		fmt.Println("Error: ", err)
		os.Exit(1)
	}
}

var runCmd = &cli.Command{
	Name: "run",
	Action: func(cctx *cli.Context) error {
		ctx := cctx.Context
		r, err := CreateRepo(cctx.String("repo"))
		if err != nil {
			return err
		}
		err = r.Init(repo.FullNode)
		if err != nil {
			return err
		}
		stop, err := node.New(ctx,
			node.Online(),
			Repo(r),
			chainSwapOpt,
		)
		if err != nil {
			return err
		}
		defer stop(ctx)
		return nil
	},
}
