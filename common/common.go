// Copyright (c) 2019-2020 The Zcash developers
// Distributed under the MIT software license, see the accompanying
// file COPYING or https://www.opensource.org/licenses/mit-license.php .

package common

import (
	"encoding/hex"
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"github.com/adityapk00/lightwalletd/parser"
	"github.com/adityapk00/lightwalletd/walletrpc"
	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"
	"google.golang.org/protobuf/proto"
)

// 'make build' will overwrite this string with the output of git-describe (tag)
var (
	Version   = "v0.0.0.0-dev"
	GitCommit = ""
	Branch    = ""
	BuildDate = ""
	BuildUser = ""
)

type Options struct {
	GRPCBindAddr        string `json:"grpc_bind_address,omitempty"`
	GRPCLogging         bool   `json:"grpc_logging_insecure,omitempty"`
	HTTPBindAddr        string `json:"http_bind_address,omitempty"`
	TLSCertPath         string `json:"tls_cert_path,omitempty"`
	TLSKeyPath          string `json:"tls_cert_key,omitempty"`
	LogLevel            uint64 `json:"log_level,omitempty"`
	LogFile             string `json:"log_file,omitempty"`
	ZcashConfPath       string `json:"zcash_conf,omitempty"`
	RPCUser             string `json:"rpcuser"`
	RPCPassword         string `json:"rpcpassword"`
	RPCHost             string `json:"rpchost"`
	RPCPort             string `json:"rpcport"`
	NoTLSVeryInsecure   bool   `json:"no_tls_very_insecure,omitempty"`
	GenCertVeryInsecure bool   `json:"gen_cert_very_insecure,omitempty"`
	Redownload          bool   `json:"redownload"`
	SyncFromHeight      int    `json:"sync_from_height"`
	DataDir             string `json:"data_dir"`
	PingEnable          bool   `json:"ping_enable"`
	Darkside            bool   `json:"darkside"`
	DarksideTimeout     uint64 `json:"darkside_timeout"`
}

// RawRequest points to the function to send a an RPC request to zcashd;
// in production, it points to btcsuite/btcd/rpcclient/rawrequest.go:RawRequest();
// in unit tests it points to a function to mock RPCs to zcashd.
var RawRequest func(method string, params []json.RawMessage) (json.RawMessage, error)

// Time allows time-related functions to be mocked for testing,
// so that tests can be deterministic and so they don't require
// real time to elapse. In production, these point to the standard
// library `time` functions; in unit tests they point to mock
// functions (set by the specific test as required).
// More functions can be added later.
var Time struct {
	Sleep func(d time.Duration)
	Now   func() time.Time
}

// Log as a global variable simplifies logging
var Log *logrus.Entry

// Metrics as a global object to simplify things
var Metrics *PrometheusMetrics

// The following are JSON zcashd rpc requests and replies.
type (
	// zcashd rpc "getblockchaininfo"
	Upgradeinfo struct {
		// unneeded fields can be omitted
		ActivationHeight int
		Status           string // "active"
	}
	ConsensusInfo struct { // consensus branch IDs
		Nextblock string // example: "e9ff75a6" (canopy)
		Chaintip  string // example: "e9ff75a6" (canopy)
	}
	ZcashdRpcReplyGetblockchaininfo struct {
		Chain           string
		Upgrades        map[string]Upgradeinfo
		Blocks          int
		BestBlockHash   string
		Consensus       ConsensusInfo
		EstimatedHeight int
	}

	// zcashd rpc "getinfo"
	ZcashdRpcReplyGetinfo struct {
		Build      string
		Subversion string
	}

	// zcashd rpc "getaddresstxids"
	ZcashdRpcRequestGetaddresstxids struct {
		Addresses []string `json:"addresses"`
		Start     uint64   `json:"start"`
		End       uint64   `json:"end"`
	}

	// zcashd rpc "z_gettreestate"
	ZcashdRpcReplyGettreestate struct {
		Height  int
		Hash    string
		Time    uint32
		Sapling struct {
			Commitments struct {
				FinalState string
			}
			SkipHash string
		}
		Orchard struct {
			Commitments struct {
				FinalState string
			}
			SkipHash string
		}
	}

	// zcashd rpc "getrawtransaction txid 1" (1 means verbose), there are
	// many more fields but these are the only ones we current need.
	ZcashdRpcReplyGetrawtransaction struct {
		Hex    string
		Height int
	}

	// zcashd rpc "getaddressbalance"
	ZcashdRpcRequestGetaddressbalance struct {
		Addresses []string `json:"addresses"`
	}
	ZcashdRpcReplyGetaddressbalance struct {
		Balance int64
	}

	// zcashd rpc "getaddressutxos"
	ZcashdRpcRequestGetaddressutxos struct {
		Addresses []string `json:"addresses"`
	}
	ZcashdRpcReplyGetaddressutxos struct {
		Address     string
		Txid        string
		OutputIndex int64
		Script      string
		Satoshis    uint64
		Height      int
	}

	// reply to getblock verbose=1 (json includes txid list)
	ZcashRpcReplyGetblock1 struct {
		Tx []string
	}
)

// FirstRPC tests that we can successfully reach zcashd through the RPC
// interface. The specific RPC used here is not important.
func FirstRPC() {
	retryCount := 0
	for {
		result, rpcErr := RawRequest("getblockchaininfo", []json.RawMessage{})
		if rpcErr == nil {
			if retryCount > 0 {
				Log.Warn("getblockchaininfo RPC successful")
			}
			var getblockchaininfo ZcashdRpcReplyGetblockchaininfo
			err := json.Unmarshal(result, &getblockchaininfo)
			if err != nil {
				Log.Fatalf("error parsing JSON getblockchaininfo response: %v", err)
			}
			break
		}
		retryCount++
		if retryCount > 10 {
			Log.WithFields(logrus.Fields{
				"timeouts": retryCount,
			}).Fatal("unable to issue getblockchaininfo RPC call to zcashd node")
		}
		Log.WithFields(logrus.Fields{
			"error": rpcErr.Error(),
			"retry": retryCount,
		}).Warn("error with getblockchaininfo rpc, retrying...")
		Time.Sleep(time.Duration(10+retryCount*5) * time.Second) // backoff
	}
}

func GetLightdInfo() (*walletrpc.LightdInfo, error) {
	result, rpcErr := RawRequest("getinfo", []json.RawMessage{})
	if rpcErr != nil {
		return nil, rpcErr
	}
	var getinfoReply ZcashdRpcReplyGetinfo
	err := json.Unmarshal(result, &getinfoReply)
	if err != nil {
		return nil, rpcErr
	}

	result, rpcErr = RawRequest("getblockchaininfo", []json.RawMessage{})
	if rpcErr != nil {
		return nil, rpcErr
	}
	var getblockchaininfoReply ZcashdRpcReplyGetblockchaininfo
	err = json.Unmarshal(result, &getblockchaininfoReply)
	if err != nil {
		return nil, rpcErr
	}
	// If the sapling consensus branch doesn't exist, it must be regtest
	var saplingHeight int
	if saplingJSON, ok := getblockchaininfoReply.Upgrades["76b809bb"]; ok { // Sapling ID
		saplingHeight = saplingJSON.ActivationHeight
	}

	vendor := "Zecwallet LightWalletD"
	if DarksideEnabled {
		vendor = "Zecwallet DarksideWalletD"
	}
	return &walletrpc.LightdInfo{
		Version:                 Version,
		Vendor:                  vendor,
		TaddrSupport:            true,
		ChainName:               getblockchaininfoReply.Chain,
		SaplingActivationHeight: uint64(saplingHeight),
		ConsensusBranchId:       getblockchaininfoReply.Consensus.Chaintip,
		BlockHeight:             uint64(getblockchaininfoReply.Blocks),
		GitCommit:               GitCommit,
		Branch:                  Branch,
		BuildDate:               BuildDate,
		BuildUser:               BuildUser,
		EstimatedHeight:         uint64(getblockchaininfoReply.EstimatedHeight),
		ZcashdBuild:             getinfoReply.Build,
		ZcashdSubversion:        getinfoReply.Subversion,
	}, nil
}

func getBestBlockHash() ([]byte, error) {
	result, rpcErr := RawRequest("getbestblockhash", []json.RawMessage{})
	if rpcErr != nil {
		return nil, rpcErr
	}
	var hash string
	err := json.Unmarshal(result, &hash)
	if err != nil {
		return nil, err
	}
	hashbytes, err := hex.DecodeString(hash)
	if err != nil {
		return nil, err
	}

	return parser.Reverse(hashbytes), nil
}

func getBlockFromRPC(height int) (*walletrpc.CompactBlock, error) {
	params := make([]json.RawMessage, 2)
	heightJSON, err := json.Marshal(strconv.Itoa(height))
	if err != nil {
		Log.Fatal("getBlockFromRPC bad height argument", height, err)
	}
	params[0] = heightJSON
	params[1] = json.RawMessage("0") // non-verbose (raw hex)
	result, rpcErr := RawRequest("getblock", params)

	// For some reason, the error responses are not JSON
	if rpcErr != nil {
		// Check to see if we are requesting a height the zcashd doesn't have yet
		if (strings.Split(rpcErr.Error(), ":"))[0] == "-8" {
			return nil, nil
		}
		return nil, errors.Wrap(rpcErr, "error requesting block")
	}

	var blockDataHex string
	err = json.Unmarshal(result, &blockDataHex)
	if err != nil {
		return nil, errors.Wrap(err, "error reading JSON response")
	}

	blockData, err := hex.DecodeString(blockDataHex)
	if err != nil {
		return nil, errors.Wrap(err, "error decoding getblock output")
	}

	block := parser.NewBlock()
	rest, err := block.ParseFromSlice(blockData)
	if err != nil {
		return nil, errors.Wrap(err, "error parsing block")
	}
	if len(rest) != 0 {
		return nil, errors.New("received overlong message")
	}

	if block.GetHeight() != height {
		return nil, errors.New("received unexpected height block")
	}

	// `block.ParseFromSlice` correctly parses blocks containing v5 transactions, but
	// incorrectly computes the IDs of the v5 transactions. We temporarily paper over this
	// bug by fetching the correct txids via a second getblock RPC call.
	// https://github.com/zcash/lightwalletd/issues/392
	{
		params[1] = json.RawMessage("1") // JSON with list of txids
		result, rpcErr := RawRequest("getblock", params)
		if rpcErr != nil {
			return nil, errors.Wrap(rpcErr, "error requesting verbose block")
		}
		var block1 ZcashRpcReplyGetblock1
		err = json.Unmarshal(result, &block1)
		if err != nil {
			return nil, err
		}
		for i, t := range block.Transactions() {
			txid, err := hex.DecodeString(block1.Tx[i])
			if err != nil {
				return nil, errors.Wrap(err, "error decoding getblock txid")
			}
			// convert from big-endian
			t.SetTxID(parser.Reverse(txid))
		}
	}

	return block.ToCompact(), nil
}

var (
	ingestorRunning  bool
	stopIngestorChan = make(chan struct{})
)

func startIngestor(c *BlockCache) {
	if !ingestorRunning {
		ingestorRunning = true
		go BlockIngestor(c, 0)
	}
}
func stopIngestor() {
	if ingestorRunning {
		ingestorRunning = false
		stopIngestorChan <- struct{}{}
	}
}

// BlockIngestor runs as a goroutine and polls zcashd for new blocks, adding them
// to the cache. The repetition count, rep, is nonzero only for unit-testing.
func BlockIngestor(c *BlockCache, rep int) {
	lastLog := Time.Now()
	lastHeightLogged := 0

	// Start listening for new blocks
	for i := 0; rep == 0 || i < rep; i++ {
		// stop if requested
		select {
		case <-stopIngestorChan:
			return
		default:
		}

		result, err := RawRequest("getbestblockhash", []json.RawMessage{})
		if err != nil {
			Log.WithFields(logrus.Fields{
				"error": err,
			}).Fatal("error zcashd getbestblockhash rpc")
		}
		var hashHex string
		err = json.Unmarshal(result, &hashHex)
		if err != nil {
			Log.Fatal("bad getbestblockhash return:", err, result)
		}
		lastBestBlockHash := []byte{}
		lastBestBlockHash, err = hex.DecodeString(hashHex)
		if err != nil {
			Log.Fatal("error decoding getbestblockhash", err, hashHex)
		}

		height := c.GetNextHeight()
		if string(lastBestBlockHash) == string(parser.Reverse(c.GetLatestHash())) {
			// Synced
			c.Sync()
			if lastHeightLogged != height-1 {
				lastHeightLogged = height - 1
				Log.Info("Waiting for block: ", height)
			}
			Time.Sleep(2 * time.Second)
			lastLog = Time.Now()
			continue
		}
		var block *walletrpc.CompactBlock
		block, err = getBlockFromRPC(height)
		if err != nil {
			Log.Fatal("getblock failed, will retry", err)
		}
		if block != nil && c.HashMatch(block.PrevHash) {
			if err = c.Add(height, block); err != nil {
				Log.Fatal("Cache add failed:", err)
			}
			// Don't log these too often.
			if DarksideEnabled || Time.Now().Sub(lastLog).Seconds() >= 4 {
				lastLog = Time.Now()
				Log.Info("Adding block to cache ", height, " ", displayHash(block.Hash))
			}
			continue
		}
		if height == c.GetFirstHeight() {
			c.Sync()
			Log.Info("Waiting for zcashd height to reach Sapling activation height ",
				"(", c.GetFirstHeight(), ")...")
			Time.Sleep(20 * time.Second)
			return
		}
		Log.Info("REORG: dropping block ", height-1, " ", displayHash(c.GetLatestHash()))
		c.Reorg(height - 1)
	}
}

// GetBlock returns the compact block at the requested height, first by querying
// the cache, then, if not found, will request the block from zcashd. It returns
// nil if no block exists at this height.
func GetBlock(cache *BlockCache, height int) (*walletrpc.CompactBlock, error) {
	// First, check the cache to see if we have the block
	block := cache.Get(height)
	if block != nil {
		return block, nil
	}

	// Not in the cache, ask zcashd
	block, err := getBlockFromRPC(height)
	if err != nil {
		return nil, err
	}
	if block == nil {
		// Block height is too large
		return nil, errors.New("block requested is newer than latest block")
	}
	return block, nil
}

// Filters out blocks that have spammy transactions
func FilterSpammyBlock(block *walletrpc.CompactBlock, spamFilterThreshold int) *walletrpc.CompactBlock {
	if spamFilterThreshold == 0 {
		return block
	}

	// Make a copy of the block so we can modify it
	newBlock := proto.Clone(block).(*walletrpc.CompactBlock)

	// Filter out Sapling transactions above the threshold by removing epk and ciphertext
	for _, tx := range newBlock.Vtx {
		if len(tx.Outputs)+len(tx.Actions) > spamFilterThreshold {
			// Remove epk and ciphertext for Sapling Outputs
			for _, outputs := range tx.Outputs {
				outputs.Ciphertext = nil
				outputs.Epk = nil
			}

			// Remove epk and ciphertext and nullifier for Actions
			for _, action := range tx.Actions {
				action.Ciphertext = nil
				action.EphemeralKey = nil
				action.Nullifier = nil
			}
		}
	}

	return newBlock
}

// GetBlockRange returns a sequence of consecutive blocks in the given range.
func GetBlockRange(cache *BlockCache, blockOut chan<- *walletrpc.CompactBlock, errOut chan<- error, start, end int, spamFilterThreshold int) {
	// Go over [start, end] inclusive
	low := start
	high := end
	if start > end {
		// reverse the order
		low, high = end, start
	}

	for i := low; i <= high; i++ {
		j := i
		if start > end {
			// reverse the order
			j = high - (i - low)
		}
		block, err := GetBlock(cache, j)
		if err != nil {
			errOut <- err
			return
		}
		blockOut <- FilterSpammyBlock(block, spamFilterThreshold)
	}
	errOut <- nil
}

func displayHash(hash []byte) string {
	return hex.EncodeToString(parser.Reverse(hash))
}
