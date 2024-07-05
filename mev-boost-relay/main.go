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
	"sync"
	"syscall"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/attestantio/go-eth2-client/spec/phase0"
	"github.com/flashbots/go-boost-utils/bls"
	boostSsz "github.com/flashbots/go-boost-utils/ssz"
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
	forceStartup     bool
)

func main() {
	flag.StringVar(&logLevel, "log-level", "info", "log level")
	flag.BoolVar(&logJSON, "log-json", false, "log as json")
	flag.StringVar(&apiListenAddr, "api-listen-addr", "0.0.0.0", "api listen address")
	flag.Uint64Var(&apiListenPort, "api-listen-port", 5555, "api listen port")
	flag.StringVar(&apiSecretKey, "api-secret", defaultSecretKey, "api secret")
	flag.StringVar(&beaconClientAddr, "beacon-client-addr", "http://localhost:8000", "beacon client address")
	flag.BoolVar(&forceStartup, "force", false, "force validator registration at startup")
	flag.Parse()

	log := common.LogSetup(logJSON, logLevel).WithFields(logrus.Fields{
		"service": "relay/housekeeper",
		"version": "dev",
	})

	// connect to the beacon client
	bClient := beaconclient.NewMultiBeaconClient(log, []beaconclient.IBeaconInstance{
		beaconclient.NewProdBeaconInstance(log, beaconClientAddr),
	})

	// wait until the beacon client is ready, otherwise, the api and housekeeper services
	// will fail at startup
	syncTimeoutCh := time.After(5 * time.Second)
	for {
		if _, err := bClient.BestSyncStatus(); err == nil {
			break
		}
		select {
		case <-syncTimeoutCh:
			log.Fatal("Beacon client failed to sync")
		default:
			time.Sleep(100 * time.Millisecond)
		}
	}
	log.Info("Beacon client synced")

	// compute the builder domain with the DOMAIN_APPLICATION_BUILDER + genesis fork version
	info, err := bClient.GetGenesis()
	if err != nil {
		log.WithError(err).Fatal("Failed to get genesis")
	}
	builderDomain, err := common.ComputeDomain(boostSsz.DomainTypeAppBuilder, info.Data.GenesisForkVersion, phase0.Root{}.String())
	if err != nil {
		log.WithError(err).Fatal("Failed to compute builder domain")
	}

	// start redis in-memory
	redis, err := startInMemoryRedisDatastore()
	if err != nil {
		log.WithError(err).Fatal("Failed to start in-memory redis")
	}

	// create the mockDB
	pqDB := newInmemoryDB()

	// datastore
	ds, err := datastore.NewDatastore(redis, nil, pqDB)
	if err != nil {
		log.WithError(err).Fatalf("Failed setting up prod datastore")
	}

	// Refresh the initial set of validators from the beacon node. This adds the validators
	// as known validators in the chain. (not registered yet).
	ds.RefreshKnownValidators(log, bClient, 10000000000)

	if forceStartup {
		// take all the known validators and register them as valid validators already
		// this is due some internal timelines inside housekeepr and api
		for i := 0; i < ds.NumKnownValidators(); i++ {
			pubKey, ok := ds.GetKnownValidatorPubkeyByIndex(uint64(i))
			if !ok {
				log.WithError(err).Fatalf("Failed to get known validator pubkey by index %d", i)
			}
			entry := database.ValidatorRegistrationEntry{
				ID:           int64(i),
				Pubkey:       pubKey.String(),
				FeeRecipient: "0x0000000000000000000000000000000000000000",
				Signature:    "0x000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000",
			}
			if err := pqDB.SaveValidatorRegistration(entry); err != nil {
				log.WithError(err).Fatalf("Failed to save validator registration for pubkey %s", pubKey.String())
			}
		}
	}

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
		EthNetDetails: common.EthNetworkDetails{
			Name:          "custom",
			DomainBuilder: builderDomain, // this is the only one required to validate the validator registration
		},
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

var emptyResponse = `{
	"jsonrpc": "2.0",
	"id": 1,
	"result": null
}`

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
		fmt.Fprint(w, emptyResponse)
	})

	go func() {
		if err := http.Serve(listener, nil); err != nil {
			log.Fatalf("HTTP server error: %v", err)
		}
	}()

	return listener.Addr().String(), nil
}

// inmemoryDB is an extension of the MockDB that stores the validator registry entries in memory.
type inmemoryDB struct {
	*database.MockDB

	validatorRegistryEntriesLock sync.Mutex
	validatorRegistryEntries     map[string]*database.ValidatorRegistrationEntry
}

func newInmemoryDB() *inmemoryDB {
	return &inmemoryDB{
		MockDB:                   &database.MockDB{},
		validatorRegistryEntries: make(map[string]*database.ValidatorRegistrationEntry),
	}
}

func (i *inmemoryDB) NumRegisteredValidators() (count uint64, err error) {
	return uint64(len(i.validatorRegistryEntries)), nil
}

func (i *inmemoryDB) SaveValidatorRegistration(entry database.ValidatorRegistrationEntry) error {
	i.validatorRegistryEntriesLock.Lock()
	defer i.validatorRegistryEntriesLock.Unlock()

	i.validatorRegistryEntries[entry.Pubkey] = &entry
	return nil
}

func (i *inmemoryDB) GetLatestValidatorRegistrations(timestampOnly bool) ([]*database.ValidatorRegistrationEntry, error) {
	i.validatorRegistryEntriesLock.Lock()
	defer i.validatorRegistryEntriesLock.Unlock()

	entries := make([]*database.ValidatorRegistrationEntry, 0, len(i.validatorRegistryEntries))
	for _, entry := range i.validatorRegistryEntries {
		entries = append(entries, entry)
	}
	return entries, nil
}

func (i *inmemoryDB) GetValidatorRegistration(pubkey string) (*database.ValidatorRegistrationEntry, error) {
	i.validatorRegistryEntriesLock.Lock()
	defer i.validatorRegistryEntriesLock.Unlock()

	entry, found := i.validatorRegistryEntries[pubkey]
	if !found {
		return nil, fmt.Errorf("validator registration not found")
	}
	return entry, nil
}

func (i *inmemoryDB) GetValidatorRegistrationsForPubkeys(pubkeys []string) ([]*database.ValidatorRegistrationEntry, error) {
	i.validatorRegistryEntriesLock.Lock()
	defer i.validatorRegistryEntriesLock.Unlock()

	entries := make([]*database.ValidatorRegistrationEntry, 0, len(pubkeys))
	for _, pubkey := range pubkeys {
		entry, found := i.validatorRegistryEntries[pubkey]
		if found {
			entries = append(entries, entry)
		}
	}
	return entries, nil
}
