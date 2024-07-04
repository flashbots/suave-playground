#!/bin/bash

set -o nounset -o errexit -o pipefail

source ./vars.env

# Execute the command with logs saved to a file.
#
# First parameter is log file name
# Second parameter is executable name
# Remaining parameters are passed to executable
execute_command() {
    LOG_NAME=$1
    EX_NAME=$2
    shift
    shift
    CMD="$EX_NAME $@ >> $LOG_DIR/$LOG_NAME 2>&1"
    echo "executing: $CMD"
    echo "$CMD" > "$LOG_DIR/$LOG_NAME"
    eval "$CMD &"
}

# Execute the command with logs saved to a file
# and is PID is saved to $PID_FILE.
#
# First parameter is log file name
# Second parameter is executable name
# Remaining parameters are passed to executable
execute_command_add_PID() {
    execute_command $@
    echo "$!" >> $PID_FILE
}

# STEP 1. Generate genesis.ssz and initial set of validators

rm -rf $DATADIR

DEBUG_LEVEL=${DEBUG_LEVEL:-info}
PID_FILE=$TESTNET_DIR/PIDS.pid
LOG_DIR=$DATADIR/logs

mkdir -p $LOG_DIR

NOW=`date +%s`
GENESIS_TIME=`expr $NOW + $GENESIS_DELAY`

lcli \
	new-testnet \
	--spec $SPEC_PRESET \
	--deposit-contract-address $DEPOSIT_CONTRACT_ADDRESS \
	--testnet-dir $TESTNET_DIR \
	--min-genesis-active-validator-count $GENESIS_VALIDATOR_COUNT \
	--min-genesis-time $GENESIS_TIME \
	--genesis-delay $GENESIS_DELAY \
	--genesis-fork-version $GENESIS_FORK_VERSION \
	--altair-fork-epoch $ALTAIR_FORK_EPOCH \
	--bellatrix-fork-epoch $BELLATRIX_FORK_EPOCH \
	--capella-fork-epoch $CAPELLA_FORK_EPOCH \
	--deneb-fork-epoch $DENEB_FORK_EPOCH \
	--ttd $TTD \
	--eth1-block-hash $ETH1_BLOCK_HASH \
	--eth1-id $CHAIN_ID \
	--eth1-follow-distance 128 \
	--seconds-per-slot $SECONDS_PER_SLOT \
	--seconds-per-eth1-block $SECONDS_PER_ETH1_BLOCK \
	--proposer-score-boost "$PROPOSER_SCORE_BOOST" \
	--validator-count $GENESIS_VALIDATOR_COUNT \
	--interop-genesis-state \
	--force

echo Specification and genesis.ssz generated at $TESTNET_DIR.
echo "Generating $VALIDATOR_COUNT validators concurrently... (this may take a while)"

lcli \
	insecure-validators \
	--count $VALIDATOR_COUNT \
	--base-dir $DATADIR \
	--node-count $VC_COUNT

echo Validators generated with keystore passwords at $DATADIR.

# STEP 2. Take the genesis template for geth and update the initial time

# Function to output SLOT_PER_EPOCH for mainnet or minimal
get_spec_preset_value() {
  case "$SPEC_PRESET" in
    mainnet)   echo 32 ;;
    minimal)   echo 8  ;;
    gnosis)    echo 16 ;;
    *)         echo "Unsupported preset: $SPEC_PRESET" >&2; exit 1 ;;
  esac
}

SLOT_PER_EPOCH=$(get_spec_preset_value $SPEC_PRESET)
#echo "slot_per_epoch=$SLOT_PER_EPOCH"

genesis_file=./genesis.json

# Update future hardforks time in the EL genesis file based on the CL genesis time
GENESIS_TIME=$(lcli pretty-ssz --spec $SPEC_PRESET --testnet-dir $TESTNET_DIR BeaconState $TESTNET_DIR/genesis.ssz | jq '.genesis_time' --raw-output)
#echo $GENESIS_TIME
CAPELLA_TIME=$((GENESIS_TIME + (CAPELLA_FORK_EPOCH * $SLOT_PER_EPOCH * SECONDS_PER_SLOT)))
#echo $CAPELLA_TIME
sed -i '' 's/"shanghaiTime".*$/"shanghaiTime": '"$CAPELLA_TIME"',/g' "$genesis_file"
CANCUN_TIME=$((GENESIS_TIME + (DENEB_FORK_EPOCH * $SLOT_PER_EPOCH * SECONDS_PER_SLOT)))
#echo $CANCUN_TIME
sed -i '' 's/"cancunTime".*$/"cancunTime": '"$CANCUN_TIME"',/g' "$genesis_file"

# STEP 3. Start GETH, get the PID and store it

lcli pretty-ssz --spec $SPEC_PRESET --testnet-dir $TESTNET_DIR BeaconState $TESTNET_DIR/genesis.ssz | jq ".genesis_validators_root" --raw-output > $DATADIR/genesis_validators_root.txt

# Create a predefined JWT token
echo "04592280e1778419b7aa954d43871cb2cfb2ebda754fb735e8adeb293a88f9bf" > $DATADIR/jwtsecret

# Initialize and start GETH
#geth init \
#    --datadir $DATADIR/geth_datadir \
#    $genesis_file

#execute_command_add_PID "geth.log" "geth" \
#    --datadir "$DATADIR/geth_datadir" \
#    --ipcdisable \
#    --http \
#    --http.api="engine,eth,web3,net,debug" \
#    --networkid="$CHAIN_ID" \
#    --syncmode=full \
#    --port 7000 \
#    --http.port 6000 \
#    --authrpc.port 5000

execute_command_add_PID "reth.log" "reth" \
	node \
	--chain $genesis_file \
	--datadir "$DATADIR/reth_datadir" \
    --ipcdisable \
	--http \
	--http.port 6000 \
	--authrpc.port 5000 \
	--authrpc.jwtsecret $DATADIR/jwtsecret

# Start BEACON NODE
execute_command_add_PID "beacon_node.log" "lighthouse" \
	--debug-level $DEBUG_LEVEL \
	bn \
	--datadir "$DATADIR/node_1" \
	--testnet-dir $TESTNET_DIR \
	--enable-private-discovery \
    --disable-peer-scoring \
	--staking \
    --http-allow-sync-stalled \
	--enr-address 127.0.0.1 \
	--enr-udp-port 9000 \
	--enr-tcp-port 9000 \
	--enr-quic-port 9100 \
	--port 9000 \
	--quic-port 9100 \
	--http-port 8000 \
	--disable-packet-filter \
	--target-peers 0 \
    --execution-endpoint http://localhost:5000 \
    --execution-jwt $DATADIR/jwtsecret

# Start VALIDATOR
execute_command_add_PID "validator.log" "lighthouse" \
	--debug-level $DEBUG_LEVEL \
	vc \
	--datadir "$DATADIR/node_1" \
	--testnet-dir $TESTNET_DIR \
	--init-slashing-protection \
	--beacon-nodes http://localhost:8000 \
	--suggested-fee-recipient 0x690B9A9E9aa1C9dB991C7721a92d351Db4FaC990

# Start mev-relay

# Start mev-boost
