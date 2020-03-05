package niaucchi4

import (
	"bytes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	mrand "math/rand"
	"time"

	"github.com/ethereum/go-ethereum/rlp"
	"github.com/geph-official/geph2/libs/c25519"
	"golang.org/x/crypto/chacha20poly1305"
	"golang.org/x/crypto/curve25519"
)

var masterSec = make([]byte, 32)

func genSK(seed []byte) [32]byte {
	return c25519.GenSKWithSeed(append(seed, masterSec...))
}

func init() {
	rand.Read(masterSec)
}

func hm(m, k []byte) []byte {
	h := hmac.New(sha256.New, k)
	h.Write(m)
	return h.Sum(nil)
}

type tunstate struct {
	enc              cipher.AEAD
	dec              cipher.AEAD
	ss               []byte
	isserv           bool
	replayProtection bool
	rw               replayWindow
}

func (ts *tunstate) deriveKeys(ss []byte) {
	//log.Printf("deriving keys from shared state %x", ss[:5])
	ts.ss = ss
	upcrypt := aead(hm(ss, []byte("up")))
	dncrypt := aead(hm(ss, []byte("dn")))
	if ts.isserv {
		ts.enc = dncrypt
		ts.dec = upcrypt
	} else {
		ts.enc = upcrypt
		ts.dec = dncrypt
	}
}

func (ts *tunstate) Decrypt(pkt []byte) (bts []byte, err error) {
	ns := ts.dec.NonceSize()
	if len(pkt) < ns {
		err = errors.New("WAT")
		return
	}
	bts, err = ts.dec.Open(nil, pkt[:ns], pkt[ns:], nil)
	if err != nil {
		return
	}
	if ts.replayProtection {
		var e2epkt e2ePacket
		err = rlp.DecodeBytes(bts, &e2epkt)
		if err != nil {
			return
		}
		if !ts.rw.check(e2epkt.Sn) {
			err = errors.New("blocking replay")
			return
		}
	}
	return
}

func (ts *tunstate) Encrypt(pkt []byte) (ctext []byte) {
	nonceb := make([]byte, ts.enc.NonceSize())
	rand.Read(nonceb)
	ctext = ts.enc.Seal(nonceb, nonceb, pkt, nil)
	return
}

type prototun struct {
	mySK   [32]byte
	cookie []byte
}

func (pt *prototun) realize(response []byte, isserv bool, replayProtection bool) (ts *tunstate, err error) {
	// decode their hello
	var theirHello helloPkt
	err = binary.Read(bytes.NewReader(response), binary.BigEndian, &theirHello)
	if err != nil {
		return
	}
	// create possible nowcookies
	for i := -30; i < 30; i++ {
		// derive nowcookie
		nowcookie := hm(pt.cookie, []byte(fmt.Sprintf("%v", time.Now().Unix()/30+int64(i))))
		//log.Printf("trying nowcookie %x", nowcookie[:5])
		boo := aead(hm(nowcookie, theirHello.Nonce[:]))
		theirPK, e := boo.
			Open(nil, make([]byte, boo.NonceSize()), theirHello.EncPK[:], nil)
		if e != nil {
			continue
		}
		var sharedsec [32]byte
		var theirPKf [32]byte
		copy(theirPKf[:], theirPK)
		if isserv {
			pt.mySK = genSK(theirPKf[:])
		}
		curve25519.ScalarMult(&sharedsec, &pt.mySK, &theirPKf)
		// make ts
		ts = &tunstate{
			isserv:           isserv,
			replayProtection: replayProtection,
		}
		ts.deriveKeys(sharedsec[:])
		return
	}
	err = errors.New("none of the cookies work")
	return
}

func newproto(cookie []byte) (pt *prototun) {
	//log.Printf("newproto with cookie = %x and nowcookie = %x", cookie[:5], nowcookie[:5])
	// generate keys
	sk := c25519.GenSK()
	pt = &prototun{
		mySK:   sk,
		cookie: cookie,
	}
	return
}

func (pt *prototun) genHello() []byte {
	var pk [32]byte
	curve25519.ScalarBaseMult(&pk, &pt.mySK)
	// derive nowcookie
	nowcookie := hm(pt.cookie, []byte(fmt.Sprintf("%v", time.Now().Unix()/30)))
	// create hello
	nonce := make([]byte, 32)
	rand.Read(nonce)
	crypter := aead(hm(nowcookie, nonce))
	encpk := crypter.
		Seal(nil, make([]byte, crypter.NonceSize()), pk[:], nil)
	if len(encpk) != 32+crypter.Overhead() {
		panic("encpk not right bytes long")
	}
	// form the pkt
	var tosend helloPkt
	copy(tosend.Nonce[:], nonce)
	copy(tosend.EncPK[:], encpk)
	buf := new(bytes.Buffer)
	binary.Write(buf, binary.BigEndian, tosend)
	padd := make([]byte, mrand.Int()%1000)
	mrand.Read(padd)
	buf.Write(padd)
	return buf.Bytes()
}

func aead(key []byte) cipher.AEAD {
	a, e := chacha20poly1305.NewX(key)
	if e != nil {
		panic(e)
	}
	return a
}

type helloPkt struct {
	Nonce [32]byte
	EncPK [48]byte
}
