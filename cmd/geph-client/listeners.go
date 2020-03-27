package main

import (
	"fmt"
	"io/ioutil"
	"net"
	"net/http"
	"time"

	log "github.com/sirupsen/logrus"

	"github.com/elazarl/goproxy"
	"github.com/geph-official/geph2/cmd/geph-client/chinalist"
	"github.com/geph-official/geph2/libs/cwl"
	"github.com/geph-official/geph2/libs/tinysocks"
	"golang.org/x/time/rate"
)

func listenStats() {
	log.Infoln("STATS on", statsAddr)
	// spin up stats server
	statsMux := http.NewServeMux()
	statsServ := &http.Server{
		Addr:         statsAddr,
		Handler:      statsMux,
		ReadTimeout:  time.Minute,
		WriteTimeout: time.Minute,
	}
	statsMux.HandleFunc("/proxy.pac", handleProxyPac)
	statsMux.HandleFunc("/kill", handleKill)
	statsMux.HandleFunc("/", handleStats)
	statsMux.HandleFunc("/logs", handleLogs)
	statsMux.HandleFunc("/debugpack", handleDebugPack)
	statsMux.HandleFunc("/stacktrace", handleStacktrace)
	err := statsServ.ListenAndServe()
	if err != nil {
		panic(err)
	}
}

func listenHTTP() {
	log.Infoln("HTTP on", httpAddr)
	// HTTP proxy
	srv := goproxy.NewProxyHttpServer()
	srv.Tr = &http.Transport{
		Dial: func(n, d string) (net.Conn, error) {
			conn, err := dialTun(d)
			if err != nil {
				return nil, err
			}
			conn.(*net.TCPConn).SetKeepAlive(false)
			conn.(*net.TCPConn).SetWriteBuffer(16384)
			return conn, nil
		},
		IdleConnTimeout: time.Second * 60,
		Proxy:           nil,
	}
	blankLogger := log.New()
	blankLogger.SetOutput(ioutil.Discard)
	srv.Logger = blankLogger
	proxServ := &http.Server{
		Addr:        httpAddr,
		Handler:     srv,
		ReadTimeout: time.Minute * 5,
		IdleTimeout: time.Minute * 5,
	}
	err := proxServ.ListenAndServe()
	if err != nil {
		panic(err.Error())
	}
}

func listenSocks() {
	listener, err := net.Listen("tcp", socksAddr)
	if err != nil {
		panic(err)
	}
	semaphore := make(chan bool, 512)
	downLimit := rate.NewLimiter(rate.Inf, 10000000)
	upLimit := rate.NewLimiter(rate.Inf, 1000*1000)
	useStats(func(sc *stats) {
		if sc.Tier == "free" {
			upLimit = rate.NewLimiter(100*1000, 1000*1000)
		}
	})
	log.Infoln("SOCKS5 on", socksAddr)
	for {
		cl, err := listener.Accept()
		if err != nil {
			panic(err)
		}
		go func() {
			defer cl.Close()
			cl.(*net.TCPConn).SetKeepAlive(false)
			select {
			case semaphore <- true:
				defer func() {
					<-semaphore
				}()
			default:
				return
			}
			cmd, rmAddrProt, err := tinysocks.ReadRequest(cl)
			if err != nil {
				log.Debugln("Bad SOCKS5 request:", err)
				return
			}
			if cmd != tinysocks.CmdConnect {
				log.Debugln("Unsupported command:", cmd)
				tinysocks.CompleteRequestTCP(7, cl)
				return
			}
			rmAddr := rmAddrProt.String()
			host, port, err := net.SplitHostPort(rmAddr)
			if realName := fakeIPToName(host); realName != "" {
				rmAddr = net.JoinHostPort(realName, port)
				host = realName
			}
			var remote net.Conn
			var ok bool
			if bypassHost(host) {
				remote, err = net.Dial("tcp", rmAddr)
				if err != nil {
					log.Printf("[%v] failed to bypass %v", len(semaphore), remote)
					tinysocks.CompleteRequestTCP(5, cl)
					return
				}
				log.Debugf("[%v] BYPASSED %v", len(semaphore), rmAddr)
				remote.(*net.TCPConn).SetKeepAlive(false) // app responsibility
			} else {
				start := time.Now()
				remote, ok = sWrap.DialCmd("proxy", rmAddr)
				if !ok {
					return
				}
				defer remote.Close()
				key := fmt.Sprintf("%v//%v", remote.RemoteAddr(), remote.LocalAddr())
				incrCounter(key)
				defer decrCounter(key)
				ping := time.Since(start)
				log.Debugf("[%v] opened %v in %vms through %v", len(semaphore), rmAddr, ping.Milliseconds(), remote.RemoteAddr())
				useStats(func(sc *stats) {
					pmil := ping.Milliseconds()
					if time.Since(sc.PingTime).Seconds() > 30 || uint64(pmil) < sc.MinPing {
						sc.MinPing = uint64(pmil)
						sc.PingTime = time.Now()
					}
				})
				if !ok {
					tinysocks.CompleteRequestTCP(5, cl)
					return
				}
			}
			tinysocks.CompleteRequestTCP(0, cl)
			go func() {
				defer remote.Close()
				defer cl.Close()
				cwl.CopyWithLimit(remote, cl, upLimit, func(n int) {
					useStats(func(sc *stats) {
						sc.UpBytes += uint64(n)
					})
				}, time.Hour)
			}()
			cwl.CopyWithLimit(cl, remote,
				downLimit, func(n int) {
					useStats(func(sc *stats) {
						sc.DownBytes += uint64(n)
					})
				}, time.Hour)
		}()
	}
}

// TODO bypass local domains
func bypassHost(str string) bool {
	return bypassChinese && chinalist.IsChinese(str)
}
