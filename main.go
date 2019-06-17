package main

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/gertjaap/nicehashmonitor/logging"
	"github.com/gertjaap/nicehashmonitor/stratum"

	"github.com/gorilla/mux"
	"github.com/rs/cors"
	"github.com/tidwall/buntdb"
)

var db *buntdb.DB

func main() {
	var err error
	logging.SetLogLevel(int(logging.LogLevelDebug))

	getWorkChan := make(chan stratum.NotifyWork, 100)

	algorithms := []string{"scrypt", "sha256", "scryptnf", "x11", "x13", "keccak", "x15", "nist5", "neoscrypt", "lyra2re", "whirlpoolx", "qubit", "quark", "axiom", "lyra2rev2", "scryptjanenf16", "blake256r8", "blake256r14", "blake256r8vnl", "hodl", "daggerhashimoto", "decred", "cryptonight", "lbry", "equihash", "pascal", "x11gost", "sia", "blake2s", "skunk", "cryptonightv7", "cryptonightheavy", "lyra2z", "x16r", "cryptonightv8", "sha256asicboost", "zhash", "beam", "grincuckaroo29", "grincuckatoo31", "lyra2rev3", "mtp", "cryptonightr", "cuckoocycle"}
	ports := []int{3333, 3334, 3335, 3336, 3337, 3338, 3339, 3340, 3341, 3342, 3343, 3344, 3345, 3346, 3347, 3348, 3349, 3350, 3351, 3352, 3353, 3354, 3355, 3356, 3357, 3358, 3359, 3360, 3361, 3362, 3363, 3364, 3365, 3366, 3367, 3368, 3369, 3370, 3371, 3372, 3373, 3374, 3375, 3376}
	locations := []string{"eu" /*, "usa", "hk", "jp", "in", "br"*/}

	db, err = buntdb.Open("/data/data.db")
	if err != nil {
		logging.Fatal(err)
	}
	defer db.Close()
	db.CreateIndex("hash", "*", buntdb.IndexJSON("hash"))
	db.CreateIndex("server", "*", buntdb.IndexJSON("server"))

	go func() {
		for w := range getWorkChan {
			r := make([]byte, 8)
			rand.Read(r)
			workJson, _ := json.Marshal(w)
			db.Update(func(tx *buntdb.Tx) error {
				_, _, err := tx.Set(fmt.Sprintf("%d-%x", w.Time.Unix(), r), string(workJson), nil)
				return err
			})
		}
	}()

	go func() {
		for i, algo := range algorithms {
			port := ports[i]
			for _, loc := range locations {
				time.Sleep(time.Second * 1)
				server := fmt.Sprintf("stratum+tcp://%s.%s.nicehash.com:%d", algo, loc, port)
				_, err := stratum.StratumConn(server, getWorkChan)
				if err != nil {
					logging.Fatal(err)
				}
			}
		}
	}()

	r := mux.NewRouter()
	r.HandleFunc("/listByHash/{hash}", ListByHash).Methods("GET")
	r.HandleFunc("/listByServer/{server}", ListByServer).Methods("GET")
	r.HandleFunc("/listByServer/{server}/{start}/{num}", ListByServerStartEnd).Methods("GET")
	r.HandleFunc("/list", List).Methods("GET")
	r.HandleFunc("/list/{start}/{num}", ListStartEnd).Methods("GET")

	srv := &http.Server{
		Handler: cors.Default().Handler(r),
		Addr:    ":8915",
		// Good practice: enforce timeouts for servers you create!
		WriteTimeout: 15 * time.Second,
		ReadTimeout:  15 * time.Second,
	}
	panic(srv.ListenAndServe())
}

func List(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Location", "/list/0/50")
	w.WriteHeader(200)
}

func ListStartEnd(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	start, err := strconv.Atoi(vars["start"])
	if err != nil {
		writeError(w, err)
		return
	}
	num, err := strconv.Atoi(vars["num"])
	if err != nil {
		writeError(w, err)
		return
	}

	pos := 0
	result := make([]stratum.NotifyWork, 0)
	db.View(func(tx *buntdb.Tx) error {
		return tx.Descend("", func(key, value string) bool {
			if pos >= start {
				r := stratum.NotifyWork{}
				err := json.Unmarshal([]byte(value), &r)
				if err == nil {
					result = append(result, r)
				}
			}
			pos++
			if pos > start+num {
				return false
			}
			return true
		})
	})
	writeJson(w, result)
}

func ListByServer(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	server := vars["server"]

	w.Header().Set("Location", fmt.Sprintf("/listByServer/%s/0/50", server))
	w.WriteHeader(200)
}

func ListByServerStartEnd(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	server := vars["server"]
	start, err := strconv.Atoi(vars["start"])
	if err != nil {
		writeError(w, err)
		return
	}
	num, err := strconv.Atoi(vars["num"])
	if err != nil {
		writeError(w, err)
		return
	}
	result := make([]stratum.NotifyWork, 0)

	pos := 0
	db.View(func(tx *buntdb.Tx) error {
		return tx.DescendRange("server", fmt.Sprintf(`{"server":"%sz"}`, server), fmt.Sprintf(`{"server":"%s"}`, server), func(key, value string) bool {
			if pos >= start {
				r := stratum.NotifyWork{}
				err := json.Unmarshal([]byte(value), &r)
				if err == nil {
					result = append(result, r)
				}
			}
			pos++
			if pos > start+num {
				return false
			}
			return true
		})
	})

	writeJson(w, result)
}

func ListByHash(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	hash := vars["hash"]

	result := make([]stratum.NotifyWork, 0)

	db.View(func(tx *buntdb.Tx) error {
		return tx.AscendEqual("hash", fmt.Sprintf(`{"hash":"%s"}`, hash), func(key, value string) bool {
			r := stratum.NotifyWork{}
			err := json.Unmarshal([]byte(value), &r)
			if err == nil {
				result = append(result, r)
			}
			return true
		})
	})

	writeJson(w, result)
}

func writeJson(w http.ResponseWriter, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(200)
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, err error) {
	w.WriteHeader(500)
	fmt.Fprintf(w, "%s", err.Error())
}
