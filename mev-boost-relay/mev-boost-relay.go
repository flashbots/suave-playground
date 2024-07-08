package mevboostrelay

import (
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"math/rand"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
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
)

var defaultSecretKey = "5eae315483f028b5cdd5d1090ff0c7618b18737ea9bf3c35047189db22835c48"

type Config struct {
	ApiListenAddr    string
	ApiListenPort    uint64
	ApiSecretKey     string
	BeaconClientAddr string
	LogOutput        io.Writer
}

func DefaultConfig() *Config {
	return &Config{
		ApiListenAddr:    "127.0.0.1",
		ApiListenPort:    5555,
		ApiSecretKey:     defaultSecretKey,
		BeaconClientAddr: "http://localhost:8000",
		LogOutput:        os.Stdout,
	}
}

type MevBoostRelay struct {
	apiSrv         *api.RelayAPI
	housekeeperSrv *housekeeper.Housekeeper
}

func New(config *Config) (*MevBoostRelay, error) {
	log := common.LogSetup(false, "info")
	log.Logger.SetOutput(config.LogOutput)

	// connect to the beacon client
	bClient := beaconclient.NewMultiBeaconClient(log, []beaconclient.IBeaconInstance{
		beaconclient.NewProdBeaconInstance(log, config.BeaconClientAddr, config.BeaconClientAddr),
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
			return nil, fmt.Errorf("beacon client failed to start")
		default:
			time.Sleep(100 * time.Millisecond)
		}
	}
	log.Info("Beacon client synced")

	// compute the builder domain with the DOMAIN_APPLICATION_BUILDER + genesis fork version
	info, err := bClient.GetGenesis()
	if err != nil {
		return nil, fmt.Errorf("failed to get genesis: %w", err)
	}
	builderDomain, err := common.ComputeDomain(boostSsz.DomainTypeAppBuilder, info.Data.GenesisForkVersion, phase0.Root{}.String())
	if err != nil {
		return nil, fmt.Errorf("failed to compute builder domain: %w", err)
	}

	// start redis in-memory
	redis, err := startInMemoryRedisDatastore()
	if err != nil {
		return nil, fmt.Errorf("failed to start in-memory redis: %w", err)
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
	ds.RefreshKnownValidatorsWithoutChecks(log, bClient, 0)

	// start housekeeping service
	housekeeperOpts := &housekeeper.HousekeeperOpts{
		Log:          log.WithField("service", "housekeeper"),
		Redis:        redis,
		DB:           pqDB,
		BeaconClient: bClient,
	}

	housekeeperSrv := housekeeper.NewHousekeeper(housekeeperOpts)
	log.Info("Starting housekeeper service...")
	go func() {
		if err := housekeeperSrv.Start(); err != nil {
			log.WithError(err).Error("Housekeeper service failed")
		}
	}()

	// start a mock block validation service that always
	// returns the blocks as valids.
	apiBlockSimURL, err := startMockBlockValidationServiceServer()
	if err != nil {
		return nil, fmt.Errorf("failed to start mock block validation service: %w", err)
	}

	// decode the secret key
	envSkBytes, err := hex.DecodeString(strings.TrimPrefix(config.ApiSecretKey, "0x"))
	if err != nil {
		return nil, fmt.Errorf("incorrect secret key provided")
	}
	secretKey, err := bls.SecretKeyFromBytes(envSkBytes[:])
	if err != nil {
		return nil, fmt.Errorf("incorrect builder API secret key provided")
	}

	apiOpts := api.RelayAPIOpts{
		Log:          log.WithField("service", "api"),
		ListenAddr:   fmt.Sprintf("%s:%d", config.ApiListenAddr, config.ApiListenPort),
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
		return nil, fmt.Errorf("failed to create service")
	}

	go func() {
		if err := apiSrv.StartServer(); err != nil {
			log.WithError(err).Error("service failed")
		}
	}()

	go func() {
		// We only require to do this at startup once, because otherwise we will
		// just keep with the normal workflow of the mev-boost-relay.
		<-apiSrv.ValidatorUpdateCh()

		log.Info("Forcing validator registration at startup")

		housekeeperSrv.UpdateProposerDutiesWithoutChecks(0)
		apiSrv.UpdateProposerDutiesWithoutChecks(0)
	}()

	return &MevBoostRelay{
		apiSrv:         apiSrv,
		housekeeperSrv: housekeeperSrv,
	}, nil
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
