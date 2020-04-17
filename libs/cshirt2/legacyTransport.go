package cshirt2

import (
	"bufio"
	"bytes"
	"crypto/cipher"
	"crypto/subtle"
	"encoding/binary"
	"errors"
	"io"
	"log"
	"net"
	"sync/atomic"
	"time"

	"github.com/geph-official/geph2/libs/erand"
	pool "github.com/libp2p/go-buffer-pool"
	"golang.org/x/crypto/chacha20"
)

// generates padding, given a write size
func generatePadding(wsize int) []byte {
	// TODO improve
	if wsize > 3000 {
		return nil
	}
	return make([]byte, erand.Int(512))
}

type legacyTransport struct {
	readMAC    []byte
	readCrypt  cipher.Stream
	writeMAC   []byte
	writeCrypt cipher.Stream
	wireBuf    *bufio.Reader
	wire       net.Conn
	readbuf    bytes.Buffer

	readDeadline  atomic.Value
	writeDeadline atomic.Value

	buf [128]byte
}

func (tp *legacyTransport) Read(b []byte) (n int, err error) {
	for tp.readbuf.Len() == 0 {
		// read the mac
		macBts := tp.buf[0:16]
		_, err = io.ReadFull(tp.wireBuf, macBts)
		if err != nil {
			return
		}
		// read the *encrypted* payload length
		cryptPayloadLenBts := tp.buf[16:][:2]
		_, err = io.ReadFull(tp.wireBuf, cryptPayloadLenBts)
		if err != nil {
			return
		}
		plainPayloadLenBts := tp.buf[18:][:2]
		tp.readCrypt.XORKeyStream(plainPayloadLenBts, cryptPayloadLenBts)
		// read the encrypted payload
		cryptInnerPayloadBts := pool.GlobalPool.Get(int(binary.BigEndian.Uint16(plainPayloadLenBts)))
		defer pool.GlobalPool.Put(cryptInnerPayloadBts)
		// short timeout
		tp.wire.SetReadDeadline(time.Now().Add(time.Second * 2))
		_, err = io.ReadFull(tp.wireBuf, cryptInnerPayloadBts)
		if err != nil {
			log.Println("could not read the", len(cryptInnerPayloadBts), "bytes requested", err.Error())
			return
		}
		tp.wire.SetReadDeadline(time.Time{})
		rdead := tp.readDeadline.Load()
		if rdead != nil {
			tp.wire.SetReadDeadline(rdead.(time.Time))
		}
		// verify the MAC
		toMAC := pool.GlobalPool.Get(len(cryptPayloadLenBts) + len(cryptInnerPayloadBts))
		defer pool.GlobalPool.Put(toMAC)
		copy(toMAC, cryptPayloadLenBts)
		copy(toMAC[len(cryptPayloadLenBts):], cryptInnerPayloadBts)
		if subtle.ConstantTimeCompare(macBts, mac128(toMAC, tp.readMAC)) != 1 {
			err = errors.New("MAC error")
			return
		}
		tp.readMAC = mac256(tp.readMAC, nil)
		// decrypt the payload itself
		plainInnerPayloadBts := pool.GlobalPool.Get(len(cryptInnerPayloadBts))
		defer pool.GlobalPool.Put(plainInnerPayloadBts)
		tp.readCrypt.XORKeyStream(plainInnerPayloadBts, cryptInnerPayloadBts)
		if len(plainInnerPayloadBts) < 2 {
			err = errors.New("truncated payload")
			return
		}
		// get the non-padding part
		realLenBts := plainInnerPayloadBts[:2]
		realBts := plainInnerPayloadBts[2:][:binary.BigEndian.Uint16(realLenBts)]
		// stuff the payload into the read buffer
		tp.readbuf.Write(realBts)
	}
	n, err = tp.readbuf.Read(b)
	return
}

func (tp *legacyTransport) Write(b []byte) (n int, err error) {
	if len(b) > 65535 {
		panic("don't know what to do!")
	}
	// first generate the plaintext payload
	plainBuf := new(bytes.Buffer)
	padding := generatePadding(len(b))
	binary.Write(plainBuf, binary.BigEndian, uint16(len(padding)+len(b)+2))
	binary.Write(plainBuf, binary.BigEndian, uint16(len(b)))
	plainBuf.Write(b)
	plainBuf.Write(padding)
	// then we encrypt the payload
	cryptPayload := plainBuf.Bytes()
	tp.writeCrypt.XORKeyStream(cryptPayload, cryptPayload)
	// then we compute the MAC and ratchet forward the key
	mac := mac128(cryptPayload, tp.writeMAC)
	tp.writeMAC = mac256(tp.writeMAC, nil)
	toWrite := pool.GlobalPool.Get(len(mac) + len(cryptPayload))
	defer pool.GlobalPool.Put(toWrite)
	copy(toWrite, mac)
	copy(toWrite[len(mac):], cryptPayload)
	// then we assemble everything
	_, err = tp.wire.Write(toWrite)
	if err != nil {
		return
	}
	n = len(b)
	return
}

func (tp *legacyTransport) Close() error {
	return tp.wire.Close()
}

func (tp *legacyTransport) LocalAddr() net.Addr {
	return tp.wire.LocalAddr()
}

func (tp *legacyTransport) RemoteAddr() net.Addr {
	return tp.wire.RemoteAddr()
}

func (tp *legacyTransport) SetDeadline(t time.Time) error {
	return tp.wire.SetDeadline(t)
}

func (tp *legacyTransport) SetReadDeadline(t time.Time) error {
	tp.readDeadline.Store(t)
	return tp.wire.SetReadDeadline(t)
}

func (tp *legacyTransport) SetWriteDeadline(t time.Time) error {
	tp.writeDeadline.Store(t)
	return tp.wire.SetWriteDeadline(t)
}

func newLegacyTransport(wire net.Conn, ss []byte, isServer bool) *legacyTransport {
	tp := new(legacyTransport)
	readKey := mac256(ss, []byte("c2s"))
	writeKey := mac256(ss, []byte("c2c"))
	if !isServer {
		readKey, writeKey = writeKey, readKey
	}
	var err error
	tp.readMAC = mac256(readKey, []byte("mac"))
	tp.readCrypt, err = chacha20.NewUnauthenticatedCipher(mac256(readKey, []byte("crypt")), make([]byte, 12))
	if err != nil {
		panic(err)
	}
	tp.writeMAC = mac256(writeKey, []byte("mac"))
	tp.writeCrypt, err = chacha20.NewUnauthenticatedCipher(mac256(writeKey, []byte("crypt")), make([]byte, 12))
	if err != nil {
		panic(err)
	}
	tp.wire = wire
	tp.wireBuf = bufio.NewReader(wire)
	return tp
}
