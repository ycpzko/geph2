package main

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"math/rand"
	mrand "math/rand"
	"net"
	"net/http"
	"time"

	"github.com/ethereum/go-ethereum/rlp"
	"github.com/geph-official/geph2/libs/cshirt2"
	"github.com/patrickmn/go-cache"
)

// cache of all bridge info. string => bridgeInfo
var bridgeCache = cache.New(time.Minute*10, time.Hour)

type bridgeInfo struct {
	Cookie     []byte
	Host       string
	LastSeen   time.Time
	AllocGroup string
}

func addBridge(nfo bridgeInfo) {
	bridgeCache.SetDefault(string(nfo.Cookie), nfo)
}

// cache of bridge *mappings*. string => []string
var bridgeMapCache = cache.New(time.Hour*48, time.Hour)

func getBridges(id string) []string {
	// if mapping, ok := bridgeMapCache.Get(id); ok {
	// 	return mapping.([]string)
	// }
	itms := bridgeCache.Items()
	seed := fmt.Sprintf("%v-%v", id, time.Now())
	probability := 10.0 / float64(len(itms))
	candidates := make([][]string, 10)
	candidateWeights := make([]int, 10)
	// try 10 times
	for i := 0; i < 10; i++ {
		for k := range itms {
			h := sha256.Sum256([]byte(k + seed))
			num := binary.BigEndian.Uint32(h[:4])
			if float64(num) < probability*float64(4294967295) || true {
				candidates[i] = append(candidates[i], k)
			}
			//TODO diversity
			candidateWeights[i] = len(candidates[i])
		}
	}
	var toret []string
	trweight := -1
	for i := 0; i < 10; i++ {
		if candidateWeights[i] > trweight {
			trweight = candidateWeights[i]
			toret = candidates[i]
		}
	}
	//bridgeMapCache.SetDefault(id, toret)

	// shuffle
	rand.Shuffle(len(toret), func(i, j int) {
		toret[i], toret[j] = toret[j], toret[i]
	})
	return toret
}

func handleGetBridges(w http.ResponseWriter, r *http.Request) {
	isEphemeral := r.FormValue("type") == "ephemeral"
	exitHost := r.FormValue("exit")
	// TODO validate the ticket
	bridges := getBridges(fmt.Sprintf("%v", mrand.Int()))
	w.Header().Set("content-type", "application/json")
	seenAGs := make(map[string]bool)
	var laboo []bridgeInfo
	for _, str := range bridges {
		if vali, ok := bridgeCache.Get(str); ok {
			val := vali.(bridgeInfo)
			if !seenAGs[val.AllocGroup] {
				if isEphemeral {
					tval, err := bridgeToEphBridge(val.Host, val.Cookie, exitHost)
					if err != nil {
						log.Println("error mapping ephemeral bridge for", val.Host, err)
						continue
					}
					val.Cookie = tval.Cookie
					val.Host = tval.Bridge
				}
				seenAGs[val.AllocGroup] = true
				laboo = append(laboo, val)
			}
		}
	}
	json.NewEncoder(w).Encode(laboo)
}

func handleAddBridge(w http.ResponseWriter, r *http.Request) {
	// first get the cookie
	cookie, err := hex.DecodeString(r.FormValue("cookie"))
	if err != nil {
		log.Println("can't add bridge (bad cookie)")
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	// check the cookie
	_, pwd, _ := r.BasicAuth()
	ok, err := checkBridgeKey(pwd)
	if err != nil {
		log.Println("can't add bridge (bad DB)")
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	if !ok {
		log.Printf("can't add bridge (bad bridge key %v)", pwd)
		w.WriteHeader(http.StatusForbidden)
		return
	}
	bi := bridgeInfo{
		Cookie:     cookie,
		Host:       r.FormValue("host"),
		LastSeen:   time.Now(),
		AllocGroup: r.FormValue("allocGroup"),
	}
	if !testBridge(bi) {
		log.Println("can't add bridge (bad bridge test)")
		w.WriteHeader(http.StatusForbidden)
		return
	}
	// add the bridge
	addBridge(bi)
}

func testBridge(bi bridgeInfo) bool {
	rawconn, err := net.Dial("tcp", bi.Host)
	if err != nil {
		log.Println("bridge test failed for", bi.Host, err)
		return false
	}
	defer rawconn.Close()
	realconn, err := cshirt2.Client(bi.Cookie, rawconn)
	if err != nil {
		log.Println("bridge test failed for", bi.Host, err)
		return false
	}
	defer realconn.Close()
	realconn.SetDeadline(time.Now().Add(time.Second * 10))
	start := time.Now()
	rlp.Encode(realconn, "ping")
	var resp string
	rlp.Decode(realconn, &resp)
	if resp != "ping" {
		log.Println(bi.Host, "failed ping test!")
		return false
	}
	log.Println(bi.Host, "passed ping test in", time.Since(start))
	return true
}
