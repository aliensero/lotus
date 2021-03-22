package node

import (
	"bufio"
	"context"
	"crypto/rand"
	"fmt"
	"time"

	cborutil "github.com/filecoin-project/go-cbor-util"
	"github.com/filecoin-project/lotus/build"
	"github.com/filecoin-project/lotus/chain/exchange"
	"github.com/filecoin-project/lotus/node/hello"

	"github.com/libp2p/go-eventbus"
	"github.com/libp2p/go-libp2p"
	connmgr "github.com/libp2p/go-libp2p-connmgr"
	"github.com/libp2p/go-libp2p-core/crypto"
	"github.com/libp2p/go-libp2p-core/event"
	"github.com/libp2p/go-libp2p-core/host"
	"github.com/libp2p/go-libp2p-core/network"
	"github.com/libp2p/go-libp2p-core/peer"
	"github.com/libp2p/go-libp2p-core/peerstore"
	dht "github.com/libp2p/go-libp2p-kad-dht"
	"github.com/libp2p/go-libp2p-peerstore/pstoremem"
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
	Override(new(lp2p.BaseIpfsRouting), func(lc fx.Lifecycle, host lp2p.RawHost, validator record.Validator, nn dtypes.NetworkName) (lp2p.BaseIpfsRouting, error) {
		fmt.Printf("NetworkerName %v\n", nn)
		opts := []dht.Option{dht.Mode(dht.ModeAuto),
			dht.Validator(validator),
			dht.ProtocolPrefix(build.DhtProtocolName(nn)),
			dht.QueryFilter(dht.PublicQueryFilter),
			dht.RoutingTableFilter(dht.PublicRoutingTableFilter),
			dht.DisableProviders(),
			dht.DisableValues()}
		d, err := dht.New(
			context.TODO(), host, opts...,
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

	Override(DiscoveryHandlerKey, lp2p.DiscoveryHandler),
	Override(ConnectionManagerKey, func() (lp2p.Libp2pOpts, error) {
		cm := connmgr.NewConnManager(50, 200, 20*time.Second)
		infos, err := build.BuiltinBootstrap()
		if err != nil {
			log.Errorf("ParseAddresses error %v", err)
			return lp2p.Libp2pOpts{}, err
		}
		for _, info := range infos {
			cm.Protect(info.ID, "bootstrap")
		}

		return lp2p.Libp2pOpts{
			Opts: []libp2p.Option{libp2p.ConnectionManager(cm)},
		}, nil
	}),

	Override(RunHelloKey, func(h host.Host) error {
		h.SetStreamHandler(hello.ProtocolID, func(s network.Stream) {
			fmt.Println("streamHandler")
			var hmsg hello.HelloMessage
			if err := cborutil.ReadCborRPC(s, &hmsg); err != nil {
				fmt.Println("failed to read hello message, disconnecting", "error", err)
				_ = s.Conn().Close()
				return
			}
			fmt.Println("hello message", hmsg)
		})
		h.SetStreamHandler(exchange.ChainExchangeProtocolID, func(s network.Stream) {
			var req exchange.Request
			if err := cborutil.ReadCborRPC(bufio.NewReader(s), &req); err != nil {
				log.Warnf("failed to read block sync request: %s", err)
				return
			}
			fmt.Printf("request %v\n", req)
		})
		sub, err := h.EventBus().Subscribe(new(event.EvtPeerIdentificationCompleted), eventbus.BufSize(1024))
		if err != nil {
			return xerrors.Errorf("failed to subscribe to event bus: %w", err)
		}
		infos, err := build.BuiltinBootstrap()
		if err != nil {
			log.Errorf("ParseAddresses error %v", err)
			return err
		}
		for _, info := range infos {
			i := info
			go func() {
				err := h.Connect(context.TODO(), i)
				log.Error(err)
			}()
		}
		for evt := range sub.Out() {
			pic := evt.(event.EvtPeerIdentificationCompleted)
			log.Infof("peer info %v", pic)
		}

		return nil
	}),
	// Override(new(Ipv4), "/ip4/0.0.0.0/tcp/3333"),
	// Override(new(Ipv6), "/ip6/::/tcp/0"),
	Override(invoke(0), func(ipv4 Ipv4, ipv6 Ipv6) error {
		// lp2p.StartListening([]string{string(ipv4), string(ipv6)})
		lp2p.StartListening([]string{"/ip4/0.0.0.0/tcp/3333", "/ip6/::/tcp/0"})
		return nil
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
