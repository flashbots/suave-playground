package main

import (
	"encoding/hex"
	"flag"
	"fmt"
	"log"
	"math/rand"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/flashbots/go-boost-utils/bls"
	"github.com/flashbots/mev-boost-relay/beaconclient"
	"github.com/flashbots/mev-boost-relay/common"
	"github.com/flashbots/mev-boost-relay/database"
	"github.com/flashbots/mev-boost-relay/datastore"
	"github.com/flashbots/mev-boost-relay/services/api"
	"github.com/flashbots/mev-boost-relay/services/housekeeper"
	"github.com/sirupsen/logrus"
)

var defaultSecretKey = "5eae315483f028b5cdd5d1090ff0c7618b18737ea9bf3c35047189db22835c48"

// flags
var (
	logLevel         string
	logJSON          bool
	apiListenAddr    string
	apiListenPort    uint64
	apiSecretKey     string
	beaconClientAddr string
)

func main() {
	flag.StringVar(&logLevel, "log-level", "info", "log level")
	flag.BoolVar(&logJSON, "log-json", false, "log as json")
	flag.StringVar(&apiListenAddr, "api-listen-addr", "0.0.0.0", "api listen address")
	flag.Uint64Var(&apiListenPort, "api-listen-port", 8000, "api listen port")
	flag.StringVar(&apiSecretKey, "api-secret", defaultSecretKey, "api secret")
	flag.StringVar(&beaconClientAddr, "beacon-client-addr", "http://localhost:8000", "beacon client address")
	flag.Parse()

	log := common.LogSetup(logJSON, logLevel).WithFields(logrus.Fields{
		"service": "relay/housekeeper",
		"version": "dev",
	})

	// connect to the beacon client
	bClient := beaconclient.NewMultiBeaconClient(log, []beaconclient.IBeaconInstance{
		beaconclient.NewProdBeaconInstance(log, beaconClientAddr),
	})

	// start redis in-memory
	redis, err := startInMemoryRedisDatastore()
	if err != nil {
		log.WithError(err).Fatal("Failed to start in-memory redis")
	}

	// create the mockDB
	pqDB := &database.MockDB{}

	// start housekeeping service
	housekeeperOpts := &housekeeper.HousekeeperOpts{
		Log:          log,
		Redis:        redis,
		DB:           pqDB,
		BeaconClient: bClient,
	}

	service := housekeeper.NewHousekeeper(housekeeperOpts)
	log.Info("Starting housekeeper service...")
	go func() {
		if err := service.Start(); err != nil {
			log.WithError(err).Error("Housekeeper service failed")
		}
	}()

	// start a mock block validation service that always
	// returns the blocks as valids.
	apiBlockSimURL, err := startMockBlockValidationServiceServer()
	if err != nil {
		log.WithError(err).Fatal("Failed to start mock block validation service")
	}

	// datastore
	ds, err := datastore.NewDatastore(redis, nil, pqDB)
	if err != nil {
		log.WithError(err).Fatalf("Failed setting up prod datastore")
	}

	// decode the secret key
	envSkBytes, err := hex.DecodeString(strings.TrimPrefix(apiSecretKey, "0x"))
	if err != nil {
		log.WithError(err).Fatal("incorrect secret key provided")
	}
	secretKey, err := bls.SecretKeyFromBytes(envSkBytes[:])
	if err != nil {
		log.WithError(err).Fatal("incorrect builder API secret key provided")
	}

	apiOpts := api.RelayAPIOpts{
		Log:          log,
		ListenAddr:   fmt.Sprintf("%s:%d", apiListenAddr, apiListenPort),
		BeaconClient: bClient,
		Datastore:    ds,
		Redis:        redis,
		DB:           pqDB,
		SecretKey:    secretKey,
		// EthNetDetails: *networkInfo, // TODO
		BlockSimURL:     apiBlockSimURL,
		ProposerAPI:     true,
		BlockBuilderAPI: true,
	}
	apiSrv, err := api.NewRelayAPI(apiOpts)
	if err != nil {
		log.WithError(err).Fatal("failed to create service")
	}

	go func() {
		if err := apiSrv.StartServer(); err != nil {
			log.WithError(err).Error("service failed")
		}
	}()

	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)
	<-sigs

	log.Info("Shutting down...")
}

func startInMemoryRedisDatastore() (*datastore.RedisCache, error) {
	redisTestServer, err := miniredis.Run()
	if err != nil {
		return nil, fmt.Errorf("failed to start miniredis: %w", err)
	}
	redisService, err := datastore.NewRedisCache("", redisTestServer.Addr(), "")
	if err != nil {
		return nil, fmt.Errorf("failed to create redis cache: %w", err)
	}
	return redisService, nil
}

func startMockBlockValidationServiceServer() (string, error) {
	// Generate a random port number between 10000 and 65535 (how likely is this?)
	rand.Seed(time.Now().UnixNano())
	port := rand.Intn(55536) + 10000

	listener, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
	if err != nil {
		return "", err
	}

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, `{"message": "This is a dummy response", "status": "ok"}`) // TODO: Return a valid JSON-RPC response here
	})

	go func() {
		if err := http.Serve(listener, nil); err != nil {
			log.Fatalf("HTTP server error: %v", err)
		}
	}()

	return listener.Addr().String(), nil
}
