package node

import (
	"context"
	"crypto/rand"
	"time"

	"github.com/filecoin-project/lotus/build"
	"github.com/filecoin-project/lotus/chain/types"

	"github.com/libp2p/go-libp2p"
	connmgr "github.com/libp2p/go-libp2p-connmgr"
	"github.com/libp2p/go-libp2p-core/crypto"
	"github.com/libp2p/go-libp2p-core/host"
	"github.com/libp2p/go-libp2p-core/peer"
	"github.com/libp2p/go-libp2p-core/peerstore"
	dht "github.com/libp2p/go-libp2p-kad-dht"
	"github.com/libp2p/go-libp2p-peerstore/pstoremem"
	pubsub "github.com/libp2p/go-libp2p-pubsub"
	record "github.com/libp2p/go-libp2p-record"
	"go.uber.org/fx"
	"golang.org/x/xerrors"

	_ "github.com/filecoin-project/lotus/lib/sigs/bls"
	_ "github.com/filecoin-project/lotus/lib/sigs/secp"
	"github.com/filecoin-project/lotus/node/modules"
	"github.com/filecoin-project/lotus/node/modules/dtypes"
	"github.com/filecoin-project/lotus/node/modules/lp2p"
)

var LIBP2PONLY = Options(

	Override(new(context.Context), context.Background()),
	Override(new(dtypes.Bootstrapper), dtypes.Bootstrapper(false)),
	Override(new(dtypes.NetworkName), func() (dtypes.NetworkName, error) {
		return "calibrationnet", nil
	}),
	Override(new(peerstore.Peerstore), pstoremem.NewPeerstore),
	Override(new(peer.ID), func(ps peerstore.Peerstore) (peer.ID, error) {
		pk, _, err := crypto.GenerateEd25519Key(rand.Reader)
		if err != nil {
			return "", err
		}
		pid, err := peer.IDFromPrivateKey(pk)
		if err != nil {
			return "", err
		}
		if err := ps.AddPrivKey(pid, pk); err != nil {
			return "", err
		}
		return pid, nil
	}),
	Override(DefaultTransportsKey, lp2p.DefaultTransports),
	Override(AddrsFactoryKey, lp2p.AddrsFactory(nil, nil)),
	Override(SmuxTransportKey, lp2p.SmuxTransport(true)),
	Override(RelayKey, lp2p.NoRelay()),
	Override(SecurityKey, lp2p.Security(true, false)),

	Override(new(lp2p.RawHost), lp2p.Host1),
	Override(new(host.Host), lp2p.RoutedHost),
	Override(new(lp2p.BaseIpfsRouting), func(ctx context.Context, lc fx.Lifecycle, host lp2p.RawHost, validator record.Validator, nn dtypes.NetworkName) (lp2p.BaseIpfsRouting, error) {
		log.Infof("NetworkerName %v\n", nn)
		opts := []dht.Option{dht.Mode(dht.ModeAuto),
			dht.Validator(validator),
			dht.ProtocolPrefix(build.DhtProtocolName(nn)),
			dht.QueryFilter(dht.PublicQueryFilter),
			dht.RoutingTableFilter(dht.PublicRoutingTableFilter),
			dht.DisableProviders(),
			dht.DisableValues()}
		d, err := dht.New(
			ctx, host, opts...,
		)

		if err != nil {
			return nil, err
		}

		lc.Append(fx.Hook{
			OnStop: func(ctx context.Context) error {
				return d.Close()
			},
		})

		return d, nil
	}),

	Override(new(record.Validator), modules.RecordValidator),
	Override(new([]peer.AddrInfo), func() []peer.AddrInfo {
		infos, err := build.BuiltinBootstrap()
		if err != nil {
			log.Error(err)
			return nil
		}
		return infos
	}),
	Override(ConnectionManagerKey, func(infos []peer.AddrInfo) (lp2p.Libp2pOpts, error) {
		cm := connmgr.NewConnManager(50, 200, 20*time.Second)
		for _, info := range infos {
			cm.Protect(info.ID, "config-prot")
		}

		return lp2p.Libp2pOpts{
			Opts: []libp2p.Option{libp2p.ConnectionManager(cm)},
		}, nil
	}),

	Override(NatPortMapKey, lp2p.NatPortMap),
	Override(BandwidthReporterKey, lp2p.BandwidthCounter),
	Override(AutoNATSvcKey, lp2p.AutoNATService),

	Override(new(Ipv4), Ipv4("/ip4/0.0.0.0/tcp/3333")),
	Override(new(Ipv6), Ipv6("/ip6/::/tcp/0")),
	Override(invoke(0), func(ipv4 Ipv4, ipv6 Ipv6, h host.Host) error {
		f := lp2p.StartListening([]string{string(ipv4), string(ipv6)})
		err := f(h)
		return err
	}),
	Override(invoke(1), func(ctx context.Context, h host.Host, infos []peer.AddrInfo) error {
		for _, info := range infos {
			err := h.Connect(ctx, info)
			log.Error(err)
		}
		return nil
	}),
	Override(invoke(2), func(ctx context.Context, h host.Host, nn dtypes.NetworkName) error {

		// topic := build.MessagesTopic(nn)
		topic := build.BlocksTopic(nn)
		log.Infof("Subscribe topic %s", topic)
		pub, err := pubsub.NewGossipSub(ctx, h)
		if err != nil {
			return err
		}
		pub.RegisterTopicValidator(topic, func(ctx context.Context, pid peer.ID, msg *pubsub.Message) pubsub.ValidationResult {
			blk, err := types.DecodeBlockMsg(msg.GetData())
			if err != nil {
				log.Error(err)
				return pubsub.ValidationReject
			}
			log.Warn("block message validate")
			msg.ValidatorData = blk
			return pubsub.ValidationAccept
		})
		sub, err := pub.Subscribe(topic)
		if err != nil {
			return err
		}

		for {
			msg, err := sub.Next(ctx)
			if err != nil {
				log.Errorf("sub.Next error %v", err)
				continue
			}
			if msg.ValidatorData != nil {
				blk := msg.ValidatorData.(*types.BlockMsg)
				log.Infof("block cid %v height %v", blk.Header.Cid(), blk.Header.Height)
			}
		}
	}),
)

func NewNoDefault(ctx context.Context, opts ...Option) (StopFunc, error) {
	settings := Settings{
		modules: map[interface{}]fx.Option{},
		invokes: make([]fx.Option, _nInvokes),
	}

	// apply module options in the right order
	if err := Options(Options(opts...))(&settings); err != nil {
		return nil, xerrors.Errorf("applying node options failed: %w", err)
	}

	// gather constructors for fx.Options
	ctors := make([]fx.Option, 0, len(settings.modules))
	for _, opt := range settings.modules {
		ctors = append(ctors, opt)
	}

	// fill holes in invokes for use in fx.Options
	for i, opt := range settings.invokes {
		if opt == nil {
			settings.invokes[i] = fx.Options()
		}
	}

	app := fx.New(
		fx.Options(ctors...),
		fx.Options(settings.invokes...),

		// fx.NopLogger,
	)

	// TODO: we probably should have a 'firewall' for Closing signal
	//  on this context, and implement closing logic through lifecycles
	//  correctly
	if err := app.Start(ctx); err != nil {
		// comment fx.NopLogger few lines above for easier debugging
		return nil, xerrors.Errorf("starting node: %w", err)
	}

	return app.Stop, nil
}

type Ipv4 string
type Ipv6 string
