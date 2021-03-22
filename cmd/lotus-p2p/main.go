package main

import (
	"os"

	"github.com/filecoin-project/lotus/lib/lotuslog"
	"github.com/filecoin-project/lotus/node"
	"github.com/filecoin-project/lotus/node/modules/dtypes"
	logging "github.com/ipfs/go-log/v2"
	"github.com/urfave/cli/v2"
)

var log = logging.Logger("main")

func main() {

	lotuslog.SetupLogLevels()
	logging.SetLogLevel("main", "DEBUG")

	local := []*cli.Command{
		runCmd,
	}

	app := &cli.App{
		Name:     "lotus-p2p",
		Commands: local,
	}

	app.Setup()
	if err := app.Run(os.Args); err != nil {
		log.Warnf("%+v", err)
		return
	}

}

var runCmd = &cli.Command{
	Name: "run",
	Flags: []cli.Flag{
		&cli.StringFlag{
			Name:  "netname",
			Usage: "Networker name",
		},
		&cli.StringFlag{
			Name:  "ipv4",
			Value: "/ip4/0.0.0.0/tcp/3334",
		},
		&cli.StringFlag{
			Name:  "ipv6",
			Value: "/ip6/::/tcp/0",
		},
	},
	Action: func(cctx *cli.Context) error {
		ctx := cctx.Context
		stopFunc, err := node.NewNoDefault(ctx,
			node.LIBP2PONLY,
			node.ApplyIf(func(s *node.Settings) bool { return cctx.String("netname") != "" }, node.Override(new(dtypes.NetworkName), cctx.String("netname"))),
			node.ApplyIf(func(s *node.Settings) bool { return cctx.String("ipv4") != "" }, node.Override(new(node.Ipv4), node.Ipv4(cctx.String("ipv4")))),
			node.ApplyIf(func(s *node.Settings) bool { return cctx.String("ipv6") != "" }, node.Override(new(node.Ipv6), node.Ipv6(cctx.String("ipv6")))),
		)
		if err != nil {
			return err
		}
		defer stopFunc(ctx)
		return nil
	},
}
