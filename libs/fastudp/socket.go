package fastudp

import (
	"io"
	"log"
	"net"
	"runtime"
	"time"

	"golang.org/x/net/ipv4"
	"golang.org/x/time/rate"
	"gopkg.in/tomb.v1"
)

const sendQuantum = 128

// Conn wraps an underlying UDPConn and batches stuff to it.
type Conn struct {
	sock  *net.UDPConn
	pconn *ipv4.PacketConn
	death *tomb.Tomb

	writeBuf chan ipv4.Message
	readBuf  []ipv4.Message
	readPtr  int
}

// NewConn creates a new Conn.
func NewConn(conn *net.UDPConn) net.PacketConn {
	err := conn.SetWriteBuffer(261244)
	if err != nil {
		panic(err)
	}
	err = conn.SetReadBuffer(261244)
	if err != nil {
		panic(err)
	}
	//return conn
	if runtime.GOOS != "linux" {
		return conn
	}
	c := &Conn{
		sock:     conn,
		pconn:    ipv4.NewPacketConn(conn),
		writeBuf: make(chan ipv4.Message, sendQuantum*2),
		death:    new(tomb.Tomb),
		readPtr:  -1,
	}
	for i := 0; i < sendQuantum; i++ {
		c.readBuf = append(c.readBuf, ipv4.Message{
			Buffers: [][]byte{malloc(2048)},
		})
	}
	go c.bkgWrite()
	return c
}

// 1000Hz syscall limiter
var limiter = rate.NewLimiter(1000, 100)

var spamLimiter = rate.NewLimiter(1, 10)

func (conn *Conn) bkgWrite() {
	defer conn.pconn.Close()
	defer conn.sock.Close()
	//
	//log.Println("bkgWrite started")
	var towrite []ipv4.Message
	for {
		//limiter.Wait(context.Background())
		select {
		case first := <-conn.writeBuf:
			towrite = append(towrite, first)
			for len(towrite) < sendQuantum {
				select {
				case next := <-conn.writeBuf:
					towrite = append(towrite, next)
				default:
					goto out
				}
			}
		out:
			for len(towrite) > 0 {
				n, err := conn.pconn.WriteBatch(towrite, 0)
				if err != nil {
					log.Println("kill", err)
					conn.death.Kill(err)
					return
				}
				for i := 0; i < n; i++ {
					free(towrite[i].Buffers[0])
					towrite[i].Buffers = nil
				}
				towrite = towrite[n:]
			}
		case <-conn.death.Dying():
			return
		}
	}
}

// ReadFrom reads a packet from the connection.
func (conn *Conn) ReadFrom(p []byte) (n int, addr net.Addr, err error) {
	// if OOB, reset
	if conn.readPtr >= len(conn.readBuf) {
		conn.readPtr = -1
	}
	// read more data if needed
	for conn.readPtr < 0 {
		// first, extend readBuf to its full size.
		conn.readBuf = conn.readBuf[:sendQuantum]
		// then, we fill readBuf as much as we can.
		fillCnt, e := conn.pconn.ReadBatch(conn.readBuf, 0)
		if e != nil {
			conn.death.Kill(e)
			err = e
			return
		}
		if fillCnt > 0 {
			// finally, we resize readBuf to its proper size.
			conn.readBuf = conn.readBuf[:fillCnt]
			conn.readPtr = 0
		}
	}
	// get the data
	gogo := conn.readBuf[conn.readPtr]
	conn.readPtr++
	copy(p, gogo.Buffers[0])
	n = gogo.N
	addr = gogo.Addr
	return
}

// WriteTo writes to a given address.
func (conn *Conn) WriteTo(p []byte, addr net.Addr) (n int, err error) {
	pCopy := malloc(len(p))
	copy(pCopy, p)
	msg := ipv4.Message{
		Buffers: [][]byte{pCopy},
		Addr:    addr,
	}
	select {
	case conn.writeBuf <- msg:
	case <-conn.death.Dying():
		err = conn.death.Err()
		return
		// default:
		// 	free(pCopy)
	}
	return len(p), nil
}

// Close closes the connection.
func (conn *Conn) Close() error {
	err := conn.sock.Close()
	conn.death.Kill(io.ErrClosedPipe)
	return err
}

// SetDeadline sets a deadline.
func (conn *Conn) SetDeadline(t time.Time) error {
	return conn.sock.SetDeadline(t)
}

// SetReadDeadline sets a read deadline.
func (conn *Conn) SetReadDeadline(t time.Time) error {
	return conn.sock.SetReadDeadline(t)
}

// SetWriteDeadline sets a write deadline.
func (conn *Conn) SetWriteDeadline(t time.Time) error {
	return conn.sock.SetWriteDeadline(t)
}

// LocalAddr returns the local address.
func (conn *Conn) LocalAddr() net.Addr {
	return conn.sock.LocalAddr()
}

// RemoteAddr returns the remote address.
func (conn *Conn) RemoteAddr() net.Addr {
	return conn.sock.RemoteAddr()
}
