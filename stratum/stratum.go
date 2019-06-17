// Copyright (c) 2016 The Decred developers.

package stratum

import (
	"bufio"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gertjaap/nicehashmonitor/logging"
	"github.com/gertjaap/nicehashmonitor/util"
	"github.com/mit-dci/lit/btcutil/base58"
)

// ErrStratumStaleWork indicates that the work to send to the pool was stale.
var ErrStratumStaleWork = fmt.Errorf("Stale work, throwing away")
var bigOne = big.NewInt(1)
var PowLimit = new(big.Int).Sub(new(big.Int).Lsh(bigOne, 236), bigOne)

// Stratum holds all the shared information for a stratum connection.
// XXX most of these should be unexported and use getters/setters.
type Stratum struct {
	// The following variables must only be used atomically.
	ValidShares   uint64
	InvalidShares uint64
	latestJobTime uint32

	sync.Mutex
	cfg       Config
	Conn      net.Conn
	Reader    *bufio.Reader
	ID        uint64
	authID    uint64
	subID     uint64
	submitIDs []uint64
	Diff      float64
	Target    *big.Int
	PoolWork  NotifyWork
	GetWork   chan NotifyWork
	Started   uint32
}

// Config holdes the config options that may be used by a stratum pool.
type Config struct {
	Pool string
	User string
	Pass string
}

// NotifyWork holds all the info recieved from a mining.notify message along
// with the Work data generate from it.
type NotifyWork struct {
	JobID  string    `json:"jobId"`
	Hash   string    `json:"hash"`
	Server string    `json:"server"`
	Time   time.Time `json:"time"`
}

// StratumMsg is the basic message object from stratum.
type StratumMsg struct {
	Method string `json:"method"`
	// Need to make generic.
	Params []string    `json:"params"`
	ID     interface{} `json:"id"`
}

// StratumRsp is the basic response type from stratum.
type StratumRsp struct {
	Method string `json:"method"`
	// Need to make generic.
	ID     interface{}      `json:"id"`
	Error  StratErr         `json:"error,omitempty"`
	Result *json.RawMessage `json:"result,omitempty"`
}

// StratErr is the basic error type (a number and a string) sent by
// the stratum server.
type StratErr struct {
	ErrNum uint64
	ErrStr string
	Result *json.RawMessage `json:"result,omitempty"`
}

// Basic reply is a reply type for any of the simple messages.
type BasicReply struct {
	ID     interface{} `json:"id"`
	Error  StratErr    `json:"error,omitempty"`
	Result bool        `json:"result"`
}

// SubscribeReply models the server response to a subscribe message.
type SubscribeReply struct {
	SubscribeID       string
	ExtraNonce1       string
	ExtraNonce2Length float64
}

// NotifyRes models the json from a mining.notify message.
type NotifyRes struct {
	JobID          string
	Hash           string
	GenTX1         string
	GenTX2         string
	MerkleBranches []string
	BlockVersion   string
	Nbits          string
	Ntime          string
	CleanJobs      bool
}

// Submit models a submission message.
type Submit struct {
	Params []string    `json:"params"`
	ID     interface{} `json:"id"`
	Method string      `json:"method"`
}

// errJsonType is an error for json that we do not expect.
var errJsonType = errors.New("Unexpected type in json.")

func sliceContains(s []uint64, e uint64) bool {
	for _, a := range s {
		if a == e {
			return true
		}
	}
	return false
}

func sliceRemove(s []uint64, e uint64) []uint64 {
	for i, a := range s {
		if a == e {
			return append(s[:i], s[i+1:]...)
		}
	}

	return s
}

func generateDummyAddress() string {
	randomHash := make([]byte, 20)
	rand.Read(randomHash)
	return base58.CheckEncode(randomHash, 0x05)
}

// StratumConn starts the initial connection to a stratum pool and sets defaults
// in the pool object.
func StratumConn(pool string, workChan chan NotifyWork) (*Stratum, error) {
	var stratum Stratum
	stratum.cfg.User = generateDummyAddress()
	stratum.cfg.Pass = "x"

	proto := "stratum+tcp://"
	pool = strings.Replace(pool, proto, "", 1)

	logging.Infof("Connecting to %s as %s\n", pool, stratum.cfg.User)

	var conn net.Conn
	var err error
	conn, err = net.Dial("tcp", pool)
	if err != nil {
		return nil, err
	}
	logging.Infof("Connected to %s\n", pool)

	stratum.ID = 1
	stratum.Conn = conn
	stratum.cfg.Pool = pool

	// We will set it for sure later but this really should be the value and
	// setting it here will prevent so incorrect matches based on the
	// default 0 value.
	stratum.authID = 2

	// Target for share is 1 unless we hear otherwise.
	stratum.Diff = 1
	stratum.Target, err = util.DiffToTarget(stratum.Diff, PowLimit)
	if err != nil {
		return nil, err
	}
	stratum.Reader = bufio.NewReader(stratum.Conn)
	go stratum.Listen()

	logging.Infof("Subscribing to %s\n", pool)
	err = stratum.Subscribe()
	if err != nil {
		return nil, err
	}
	logging.Infof("Authorizing on %s\n", pool)
	err = stratum.Auth()
	if err != nil {
		return nil, err
	}

	stratum.GetWork = workChan

	stratum.Started = uint32(time.Now().Unix())

	return &stratum, nil
}

// Reconnect reconnects to a stratum server if the connection has been lost.
func (s *Stratum) Reconnect() error {
	var conn net.Conn
	var err error

	s.Conn.Close()

	s.cfg.User = generateDummyAddress()

	logging.Infof("Reconnecting to %s as %s\n", s.cfg.Pool, s.cfg.User)

	conn, err = net.Dial("tcp", s.cfg.Pool)
	if err != nil {
		return err
	}
	s.Conn = conn
	s.Reader = bufio.NewReader(s.Conn)
	err = s.Subscribe()
	if err != nil {
		return nil
	}
	// XXX Do I really need to re-auth here?
	err = s.Auth()
	if err != nil {
		return nil
	}

	// If we were able to reconnect, restart counter
	s.Started = uint32(time.Now().Unix())

	go s.Listen()

	return nil
}

// Listen is the listener for the incoming messages from the stratum pool.
func (s *Stratum) Listen() {

	for {
		logging.Infof("Reading from reader for [%s]\n", s.cfg.Pool)
		result, err := s.Reader.ReadString('\n')
		if err != nil {
			logging.Errorf("[%s] Error reading from transport : %s\n", s.cfg.Pool, err.Error())
			if err == io.EOF {
				for err != nil {
					time.Sleep(time.Second * 60)
					err = s.Reconnect()
				}
			}
			s.Conn.Close()
			return
		}

		logging.Infof("Received [%s]\n", result)

		resp, err := s.Unmarshal([]byte(result))
		if err != nil {
			logging.Error(err)
			continue
		}

		switch resp.(type) {
		case *BasicReply:
			s.handleBasicReply(resp)
		case StratumMsg:
			s.handleStratumMsg(resp)
		case NotifyRes:
			s.handleNotifyRes(resp)
		case *SubscribeReply:
			s.handleSubscribeReply(resp)
		default:
		}
	}
}

func (s *Stratum) handleBasicReply(resp interface{}) {
	s.Lock()
	defer s.Unlock()
	aResp := resp.(*BasicReply)

	if int(aResp.ID.(uint64)) == int(s.authID) {
		if !aResp.Result {
			logging.Error("Auth failure.")
		}
	}
	if sliceContains(s.submitIDs, aResp.ID.(uint64)) {
		if aResp.Result {
			atomic.AddUint64(&s.ValidShares, 1)
		} else {
			atomic.AddUint64(&s.InvalidShares, 1)
			logging.Error("Share rejected: ", aResp.Error.ErrStr)
		}
		s.submitIDs = sliceRemove(s.submitIDs, aResp.ID.(uint64))
	}
}

func (s *Stratum) handleStratumMsg(resp interface{}) {
	nResp := resp.(StratumMsg)
	// Too much is still handled in unmarshaler.  Need to
	// move stuff other than unmarshalling here.
	switch nResp.Method {
	case "client.show_message":
	case "client.reconnect":
		wait, err := strconv.Atoi(nResp.Params[2])
		if err != nil {
			logging.Error(err)
			return
		}
		time.Sleep(time.Duration(wait) * time.Second)
		pool := nResp.Params[0] + ":" + nResp.Params[1]
		s.cfg.Pool = pool
		err = s.Reconnect()
		if err != nil {
			logging.Error(err)
			// XXX should just die at this point
			// but we don't really have access to
			// the channel to end everything.
			return
		}

	case "client.get_version":
		msg := StratumMsg{
			Method: nResp.Method,
			ID:     nResp.ID,
			Params: []string{"miner/1.0"},
		}
		m, err := json.Marshal(msg)
		if err != nil {
			logging.Error(err)
			return
		}
		_, err = s.Conn.Write(m)
		if err != nil {
			logging.Error(err)
			return
		}
		_, err = s.Conn.Write([]byte("\n"))
		if err != nil {
			logging.Error(err)
			return
		}
	}
}

func (s *Stratum) handleNotifyRes(resp interface{}) {
	s.Lock()
	defer s.Unlock()
	nResp := resp.(NotifyRes)
	s.PoolWork.JobID = nResp.JobID

	s.PoolWork.Hash = util.RevHash(nResp.Hash)
	s.PoolWork.Server = s.cfg.Pool
	s.PoolWork.Time = time.Now()
	s.GetWork <- s.PoolWork
}

func (s *Stratum) handleSubscribeReply(resp interface{}) {

}

// Auth sends a message to the pool to authorize a worker.
func (s *Stratum) Auth() error {
	msg := StratumMsg{
		Method: "mining.authorize",
		ID:     s.ID,
		Params: []string{s.cfg.User, s.cfg.Pass},
	}
	// Auth reply has no method so need a way to identify it.
	// Ugly, but not much choice.
	id, ok := msg.ID.(uint64)
	if !ok {
		return errJsonType
	}
	s.authID = id
	s.ID++
	m, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	_, err = s.Conn.Write(m)
	if err != nil {
		return err
	}
	_, err = s.Conn.Write([]byte("\n"))
	if err != nil {
		return err
	}
	return nil
}

// Subscribe sends the subscribe message to get mining info for a worker.
func (s *Stratum) Subscribe() error {
	msg := StratumMsg{
		Method: "mining.subscribe",
		ID:     s.ID,
		Params: []string{"miner/1.0"},
	}
	s.subID = msg.ID.(uint64)
	s.ID++
	m, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	_, err = s.Conn.Write(m)
	if err != nil {
		return err
	}
	_, err = s.Conn.Write([]byte("\n"))
	if err != nil {
		return err
	}
	return nil
}

// Unmarshal provides a json unmarshaler for the commands.
// I'm sure a lot of this can be generalized but the json we deal with
// is pretty yucky.
func (s *Stratum) Unmarshal(blob []byte) (interface{}, error) {
	s.Lock()
	defer s.Unlock()
	var (
		objmap map[string]json.RawMessage
		method string
		id     uint64
	)

	err := json.Unmarshal(blob, &objmap)
	if err != nil {
		return nil, err
	}
	// decode command
	// Not everyone has a method.
	err = json.Unmarshal(objmap["method"], &method)
	if err != nil {
		method = ""
	}
	err = json.Unmarshal(objmap["id"], &id)
	if err != nil {
		return nil, err
	}
	if id == s.authID {
		var (
			objmap      map[string]json.RawMessage
			id          uint64
			result      bool
			errorHolder []interface{}
		)
		err := json.Unmarshal(blob, &objmap)
		if err != nil {
			return nil, err
		}
		resp := &BasicReply{}

		err = json.Unmarshal(objmap["id"], &id)
		if err != nil {
			return nil, err
		}
		resp.ID = id

		err = json.Unmarshal(objmap["result"], &result)
		if err != nil {
			return nil, err
		}
		err = json.Unmarshal(objmap["error"], &errorHolder)
		if err != nil {
			return nil, err
		}
		resp.Result = result

		if errorHolder != nil {
			errN, ok := errorHolder[0].(float64)
			if !ok {
				return nil, errJsonType
			}
			errS, ok := errorHolder[1].(string)
			if !ok {
				return nil, errJsonType
			}
			resp.Error.ErrNum = uint64(errN)
			resp.Error.ErrStr = errS
		}

		return resp, nil

	}
	if id == s.subID {
		var resi []interface{}
		err := json.Unmarshal(objmap["result"], &resi)
		if err != nil {
			errOriginal := err
			var resb bool
			err := json.Unmarshal(objmap["result"], &resb)
			if err == nil && !resb {
				return nil, nil
			}

			return nil, errOriginal
		}
		resp := &SubscribeReply{}

		var objmap2 map[string]json.RawMessage
		err = json.Unmarshal(blob, &objmap2)
		if err != nil {
			return nil, err
		}

		var resJS []json.RawMessage
		err = json.Unmarshal(objmap["result"], &resJS)
		if err != nil {
			return nil, err
		}

		if len(resJS) == 0 {
			return nil, errJsonType
		}

		var msgPeak []interface{}
		err = json.Unmarshal(resJS[0], &msgPeak)
		if err != nil {
			return nil, err
		}

		if len(msgPeak) == 0 {
			return nil, errJsonType
		}

		// The pools do not all agree on what this message looks like
		// so we need to actually look at it before unmarshalling for
		// real so we can use the right form.  Yuck.
		if msgPeak[0] == "mining.notify" {
			var innerMsg []string
			err = json.Unmarshal(resJS[0], &innerMsg)
			if err != nil {
				return nil, err
			}
			resp.SubscribeID = innerMsg[1]
		} else {
			var innerMsg [][]string
			err = json.Unmarshal(resJS[0], &innerMsg)
			if err != nil {
				return nil, err
			}

			for i := 0; i < len(innerMsg); i++ {
				if innerMsg[i][0] == "mining.notify" {
					resp.SubscribeID = innerMsg[i][1]
				}
				if innerMsg[i][0] == "mining.set_difficulty" {
					// Not all pools correctly put something
					// in here so we will ignore it (we
					// already have the default value of 1
					// anyway and pool can send a new one.
					// dcr.coinmine.pl puts something that
					// is not a difficulty here which is why
					// we ignore.
				}
			}
		}
		return resp, nil
	}
	if sliceContains(s.submitIDs, id) {
		var (
			objmap      map[string]json.RawMessage
			id          uint64
			result      bool
			errorHolder []interface{}
		)
		err := json.Unmarshal(blob, &objmap)
		if err != nil {
			return nil, err
		}
		resp := &BasicReply{}

		err = json.Unmarshal(objmap["id"], &id)
		if err != nil {
			return nil, err
		}
		resp.ID = id

		err = json.Unmarshal(objmap["result"], &result)
		if err != nil {
			return nil, err
		}
		err = json.Unmarshal(objmap["error"], &errorHolder)
		if err != nil {
			return nil, err
		}
		resp.Result = result

		if errorHolder != nil {
			errN, ok := errorHolder[0].(float64)
			if !ok {
				return nil, errJsonType
			}
			errS, ok := errorHolder[1].(string)
			if !ok {
				return nil, errJsonType
			}
			resp.Error.ErrNum = uint64(errN)
			resp.Error.ErrStr = errS
		}

		return resp, nil
	}
	switch method {
	case "mining.notify":
		var resi []interface{}
		err := json.Unmarshal(objmap["params"], &resi)
		if err != nil {
			return nil, err
		}
		var nres = NotifyRes{}
		jobID, ok := resi[0].(string)
		if !ok {
			return nil, errJsonType
		}
		nres.JobID = jobID
		hash, ok := resi[1].(string)
		if !ok {
			return nil, errJsonType
		}
		nres.Hash = hash
		genTX1, ok := resi[2].(string)
		if !ok {
			return nil, errJsonType
		}
		nres.GenTX1 = genTX1
		genTX2, ok := resi[3].(string)
		if !ok {
			return nil, errJsonType
		}
		nres.GenTX2 = genTX2
		//ccminer code also confirms this
		//nres.MerkleBranches = resi[4].([]string)
		blockVersion, ok := resi[5].(string)
		if !ok {
			return nil, errJsonType
		}
		nres.BlockVersion = blockVersion
		nbits, ok := resi[6].(string)
		if !ok {
			return nil, errJsonType
		}
		nres.Nbits = nbits
		ntime, ok := resi[7].(string)
		if !ok {
			return nil, errJsonType
		}
		nres.Ntime = ntime
		cleanJobs, ok := resi[8].(bool)
		if !ok {
			return nil, errJsonType
		}
		nres.CleanJobs = cleanJobs
		return nres, nil

	case "mining.set_difficulty":
		var resi []interface{}
		err := json.Unmarshal(objmap["params"], &resi)
		if err != nil {
			return nil, err
		}

		difficulty, ok := resi[0].(float64)
		if !ok {
			return nil, errJsonType
		}
		s.Target, err = util.DiffToTarget(difficulty, PowLimit)
		if err != nil {
			return nil, err
		}
		s.Diff = difficulty
		var nres = StratumMsg{}
		nres.Method = method
		diffStr := strconv.FormatFloat(difficulty, 'E', -1, 32)
		var params []string
		params = append(params, diffStr)
		nres.Params = params
		return nres, nil

	case "client.show_message":
		var resi []interface{}
		err := json.Unmarshal(objmap["result"], &resi)
		if err != nil {
			return nil, err
		}
		msg, ok := resi[0].(string)
		if !ok {
			return nil, errJsonType
		}
		var nres = StratumMsg{}
		nres.Method = method
		var params []string
		params = append(params, msg)
		nres.Params = params
		return nres, nil

	case "client.get_version":
		var nres = StratumMsg{}
		var id uint64
		err = json.Unmarshal(objmap["id"], &id)
		if err != nil {
			return nil, err
		}
		nres.Method = method
		nres.ID = id
		return nres, nil

	case "client.reconnect":
		var nres = StratumMsg{}
		var id uint64
		err = json.Unmarshal(objmap["id"], &id)
		if err != nil {
			return nil, err
		}
		nres.Method = method
		nres.ID = id

		var resi []interface{}
		err := json.Unmarshal(objmap["params"], &resi)
		if err != nil {
			return nil, err
		}

		if len(resi) < 3 {
			return nil, errJsonType
		}
		hostname, ok := resi[0].(string)
		if !ok {
			return nil, errJsonType
		}
		p, ok := resi[1].(float64)
		if !ok {
			return nil, errJsonType
		}
		port := strconv.Itoa(int(p))
		w, ok := resi[2].(float64)
		if !ok {
			return nil, errJsonType
		}
		wait := strconv.Itoa(int(w))

		nres.Params = []string{hostname, port, wait}

		return nres, nil

	default:
		resp := &StratumRsp{}
		err := json.Unmarshal(blob, &resp)
		if err != nil {
			return nil, err
		}
		return resp, nil
	}
}

// PrepSubmit formats a mining.sumbit message from the solved work.
func (s *Stratum) SubmitDummyWork() error {

	sub := Submit{}
	sub.Method = "mining.submit"

	// Format data to send off.
	s.ID++
	sub.ID = s.ID
	s.submitIDs = append(s.submitIDs, s.ID)

	timestampStr := fmt.Sprintf("%08x", time.Now().Unix())
	nonceStr := fmt.Sprintf("%08x", 5)
	xnonceStr := fmt.Sprintf("%08x", 398267)

	sub.Params = []string{s.cfg.User, s.PoolWork.JobID, xnonceStr, timestampStr,
		nonceStr}

	m, err := json.Marshal(sub)
	if err != nil {
		return err
	}
	_, err = s.Conn.Write(m)
	if err != nil {
		return err
	}
	_, err = s.Conn.Write([]byte("\n"))
	if err != nil {
		return err
	}
	return nil
}
